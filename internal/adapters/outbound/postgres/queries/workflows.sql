-- Queries backing WorkflowStore ("workflow execution engine",
-- §25.6/§25.7/§25.8), the first real reader/writer of §25.4's own dark
-- schema (migrations/000057_workflows.up.sql). internal/app/workflowengine
-- is this file's only caller: it resolves which (lane, repo) binding and
-- WorkflowDefinition govern a session's current turn, tracks the run/
-- step-run ledger these queries persist, and consults
-- internal/domain/workflow.NextStep on every posted/derived step outcome
-- -- never reimplementing that decision here.
--
-- Binding resolution (§25.4): a repo-specific row (GetWorkflowBindingForRepo)
-- always wins when one exists; GetGlobalWorkflowBinding is the guaranteed
-- fallback (seeded by migration 000057 for every lane, so this SELECT is
-- never truly expected to return zero rows in a correctly-migrated
-- database -- see workflowengine's own fail-open handling for the
-- defensive case where it somehow does).
--
-- Definition assembly (GetWorkflowDefinition + ListWorkflowStepDefinitions +
-- ListWorkflowEdgesForDefinition): three queries, not one join, mirroring
-- domain/workflow.Definition's own shape (a definition row plus its
-- ordered steps, each carrying its own outgoing edges) -- workflowengine
-- groups edges by from_step_id in Go when assembling the domain value, the
-- same "read plain rows, assemble the domain type in the app layer" split
-- every other store in this package already follows (e.g. PlanStore never
-- constructs an internal/domain/plan value itself).
--
-- Run/step-run lifecycle (§25.6, migration 000057's own header comment):
-- GetRunningWorkflowRunForSession/GetLiveWorkflowStepRunForRun back the
-- "is there already a live run/step for this session" check
-- ResolveStepForNewTurn makes before ever starting a new run --
-- workflow_runs_one_running_per_session/workflow_step_runs_one_live_per_run
-- (migration 000057) make violating either a live constraint violation,
-- not just an application bug; this store's own CreateWorkflowRun/
-- CreateWorkflowStepRun are therefore only ever called from the branch that
-- already confirmed no live one exists. AttachTurnToWorkflowStepRun
-- backfills turn_id once the actual turn row exists (it cannot exist before
-- the step-run row is created, since ResolveStepForNewTurn must decide the
-- step BEFORE the turn insert it feeds prompt/model into) --
-- GetLiveWorkflowStepRunByTurnID is completion.go's own reverse lookup, at
-- turn-completion time.
--
-- MarkWorkflowStepRunAwaitingDecision/FinishWorkflowStepRun/
-- CompleteWorkflowRun/EscalateWorkflowRun are deliberately four narrow,
-- single-purpose statements rather than one CASE-parameterized UPDATE each
-- -- mirroring plans.sql's own ApprovePlanIfAwaitingApproval/
-- RejectPlanIfAwaitingApproval precedent (two verdicts, two statements) --
-- so finished_at's own "null while awaiting_decision" invariant
-- (migration 000057's own doc comment) is enforced by which statement runs,
-- never by a conditional expression a future edit could silently get wrong.
-- outcome_status uses COALESCE in both step-run UPDATEs so an outcome
-- ALREADY posted via the step-outcome tool (SetWorkflowStepRunOutcome
-- below) is never clobbered by the engine's own implicit-from-trigger
-- derivation at turn-completion time -- the posted value always wins.
--
-- SetWorkflowStepRunOutcome is the generic step-outcome-posting tool's own
-- write (§25.6's typed {status, summary, structuredPayload} handoff,
-- structurally mirroring internal/domain/reviewpost's existing
-- verdict-posting shape): guarded to "AND status = 'running'" so a caller
-- can only ever post onto the attempt currently actually in flight, never
-- an already-finished or awaiting-decision one -- :execrows lets the
-- handler distinguish "posted" from "no live attempt to post onto" without
-- a separate existence check.

-- name: GetWorkflowBindingForRepo :one
SELECT * FROM workflow_bindings WHERE lane = $1 AND repo_full_name = $2;

-- name: GetGlobalWorkflowBinding :one
SELECT * FROM workflow_bindings WHERE lane = $1 AND repo_full_name IS NULL;

-- name: GetWorkflowDefinition :one
SELECT * FROM workflow_definitions WHERE id = $1;

-- name: ListWorkflowStepDefinitions :many
SELECT * FROM workflow_step_definitions WHERE workflow_definition_id = $1 ORDER BY step_order;

-- name: ListWorkflowEdgesForDefinition :many
SELECT * FROM workflow_edges WHERE workflow_definition_id = $1;

-- name: GetRunningWorkflowRunForSession :one
SELECT * FROM workflow_runs WHERE session_id = $1 AND status = 'running';

-- name: GetWorkflowRun :one
SELECT * FROM workflow_runs WHERE id = $1;

-- name: CreateWorkflowRun :one
INSERT INTO workflow_runs (session_id, lane, workflow_definition_id, definition_version)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetLiveWorkflowStepRunForRun :one
SELECT * FROM workflow_step_runs
WHERE workflow_run_id = $1 AND status IN ('running', 'awaiting_decision');

-- name: GetLiveWorkflowStepRunByTurnID :one
SELECT * FROM workflow_step_runs WHERE turn_id = $1 AND status = 'running';

-- name: CreateWorkflowStepRun :one
INSERT INTO workflow_step_runs (workflow_run_id, step_definition_id)
VALUES ($1, $2)
RETURNING *;

-- name: AttachTurnToWorkflowStepRun :exec
UPDATE workflow_step_runs SET turn_id = $2, updated_at = now() WHERE id = $1;

-- name: MarkWorkflowStepRunAwaitingDecision :one
UPDATE workflow_step_runs
SET status = 'awaiting_decision', outcome_status = COALESCE(outcome_status, $2), updated_at = now()
WHERE id = $1
RETURNING *;

-- name: FinishWorkflowStepRun :one
-- status is the caller-computed terminal workflow_step_run_status
-- (completed/failed/cancelled -- never awaiting_decision, which
-- MarkWorkflowStepRunAwaitingDecision above owns exclusively).
UPDATE workflow_step_runs
SET status = $2, outcome_status = COALESCE(outcome_status, $3), finished_at = now(), updated_at = now()
WHERE id = $1
RETURNING *;

-- name: CompleteWorkflowRun :one
UPDATE workflow_runs SET status = 'completed', finished_at = now(), updated_at = now() WHERE id = $1 RETURNING *;

-- name: FailWorkflowRun :one
-- §25.9's own addition (§25.9): a winning HITL reject verdict ends the
-- run -- mirrors CompleteWorkflowRun's own shape exactly, landing on
-- 'failed' instead of 'completed' (WorkflowStepDecideResponse.runStatus's
-- own documented "'failed' after a winning reject ends the run" example).
-- Terminal (finished_at set), unlike EscalateWorkflowRun's own
-- 'needs_review' (non-terminal, waiting on a human) -- a reject is itself
-- the human's own final word, nothing further to wait on.
UPDATE workflow_runs SET status = 'failed', finished_at = now(), updated_at = now() WHERE id = $1 RETURNING *;

-- name: EscalateWorkflowRun :one
UPDATE workflow_runs SET status = 'needs_review', updated_at = now() WHERE id = $1 RETURNING *;

-- name: SetWorkflowStepRunOutcome :execrows
UPDATE workflow_step_runs
SET outcome_status = $2, outcome_summary = $3, outcome_payload = $4, updated_at = now()
WHERE id = $1 AND status = 'running';

-- §25.9 ("workflow HITL gate + circuit breaker", §25.9) own additions
-- below: the decide endpoint's lookup/guarded-decision queries, the
-- circuit breaker's own COUNT(*) attempt read (§25.5), and the escalation
-- notice's one-time claim (migrations/000058_workflow_hitl.up.sql).

-- name: GetWorkflowStepRun :one
-- Plain lookup by id -- the decide endpoint's own first read (mirrors
-- plans.Get's identical role in decideplan.go): used both to resolve the
-- target attempt before deciding it and, after the guarded UPDATE below,
-- to re-fetch its REAL current state either way (won or already decided),
-- exactly like DecidePlanOnTx's own plans.Get re-fetch.
SELECT * FROM workflow_step_runs WHERE id = $1;

-- name: CountWorkflowStepRunsForStepDefinition :one
-- §25.5's own "iteration count reads COUNT(*) on workflow_step_runs, no
-- dedicated counter column" -- scoped to ONE run (a count is never summed
-- across a session's separate runs) and ONE step definition (the step
-- about to receive a new attempt). The engine consults this ONLY when a
-- needs_fix edge is about to advance to a step that might already have
-- prior attempts in THIS run -- see internal/app/workflowengine's own
-- advance.go for the exact call site and the "count > 0 means a genuine
-- re-fire" reasoning.
SELECT COUNT(*) FROM workflow_step_runs WHERE workflow_run_id = $1 AND step_definition_id = $2;

-- DecideWorkflowStepRunApprove/Reject/Revise are three narrow,
-- single-purpose guarded UPDATEs -- mirroring plans.sql's own
-- ApprovePlanIfAwaitingApproval/RejectPlanIfAwaitingApproval precedent
-- (two verdicts, two statements) exactly, extended to three here so which
-- resulting status/decision applies to which verdict is enforced by WHICH
-- STATEMENT RUNS, never by a conditional expression a future edit could
-- silently get wrong (this file's own header comment states this
-- discipline for MarkWorkflowStepRunAwaitingDecision/FinishWorkflowStepRun
-- above; the same reasoning applies here). All three share the identical
-- "AND status = 'awaiting_decision'" guard -- :execrows lets the handler
-- distinguish "this call won the decision" from "already decided (or a
-- stale id)" without a separate existence check, exactly like
-- ApprovePlanIfAwaitingApproval's own :execrows shape. decisionText ($2)
-- is nullable: NULL for approve (ignored), optional context for reject,
-- required non-empty for revise (enforced at the application layer, per
-- WorkflowStepDecideRequest.text's own schema doc comment).
--
-- Resulting status: approve/revise both land on 'completed' (the
-- attempt's own execution genuinely finished either way -- what happens
-- NEXT, advance vs re-execute, is the decision column's own job, not the
-- status column's); reject lands on 'failed', mirroring
-- WorkflowStepDecideResponse.runStatus's own documented "'failed' after a
-- winning reject ends the run" example -- the owning run's and the
-- decided attempt's own resulting status agree by construction.

-- name: DecideWorkflowStepRunApprove :execrows
UPDATE workflow_step_runs
SET status = 'completed', decision = 'approve', decision_text = $2, decided_at = now(), decided_by = $3, finished_at = now(), updated_at = now()
WHERE id = $1 AND status = 'awaiting_decision';

-- name: DecideWorkflowStepRunReject :execrows
UPDATE workflow_step_runs
SET status = 'failed', decision = 'reject', decision_text = $2, decided_at = now(), decided_by = $3, finished_at = now(), updated_at = now()
WHERE id = $1 AND status = 'awaiting_decision';

-- name: DecideWorkflowStepRunRevise :execrows
UPDATE workflow_step_runs
SET status = 'completed', decision = 'revise', decision_text = $2, decided_at = now(), decided_by = $3, finished_at = now(), updated_at = now()
WHERE id = $1 AND status = 'awaiting_decision';

-- name: ClaimWorkflowRunEscalationNotice :execrows
-- The §25.9/§24.6-mirroring "one notice, never repeated" claim
-- (migrations/000058_workflow_hitl.up.sql's own doc comment): guarded on
-- needs_review_notified_at IS NULL, so of any number of concurrent/
-- repeated attempts to escalate the SAME run, exactly one ever claims the
-- right to enqueue the notice -- :execrows lets the caller tell "I claimed
-- it, send the notice" (1) from "already claimed/sent by an earlier
-- escalation" (0) apart, without a separate existence check.
UPDATE workflow_runs SET needs_review_notified_at = now(), updated_at = now() WHERE id = $1 AND needs_review_notified_at IS NULL;
