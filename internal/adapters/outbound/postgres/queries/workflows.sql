-- Queries backing WorkflowStore (Step 55, "workflow execution engine",
-- §25.6/§25.7/§25.8), the first real reader/writer of Step 54's own dark
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

-- name: EscalateWorkflowRun :one
UPDATE workflow_runs SET status = 'needs_review', updated_at = now() WHERE id = $1 RETURNING *;

-- name: SetWorkflowStepRunOutcome :execrows
UPDATE workflow_step_runs
SET outcome_status = $2, outcome_summary = $3, outcome_payload = $4, updated_at = now()
WHERE id = $1 AND status = 'running';
