-- Queries backing TurnStore (§4.3). Just enough to prove the pipeline end
-- to end (create + get), including exercising the
-- turns_one_processing_per_session partial unique index (§3.3).

-- name: CreateTurn :one
-- prompt/model_id/plan_mode (migrations/000018_session_repos.up.sql, Step
-- 21) are the turn's own dispatch-time inputs -- prompt/model_id are
-- nullable and plan_mode defaults false, so every EXISTING call site
-- (every prior Step's `CreateTurnParams{SessionID, Status}`) keeps
-- compiling and behaving identically: the zero-value nil/nil/false it
-- already implicitly got before this Step's own columns existed.
--
-- effort (migrations/000063_turn_session_effort.up.sql, Step 59, §29.8)
-- mirrors model_id's own shape exactly, one column over -- plain
-- positional param like model_id itself (this query's own existing style
-- for a nullable column; sqlc generates a keyed struct either way, so
-- every EXISTING call site that never sets it -- a keyed
-- CreateTurnParams{...} literal omitting Effort -- keeps compiling and
-- behaving identically: the zero value, nil, "use the default").
--
-- review_head_sha (migrations/000072_turns_review_head_sha.up.sql, §62
-- review finding C2) mirrors effort's own identical shape one column
-- further -- nil/absent for every non-review turn (every existing call
-- site), set exactly once, at creation, by the two review-turn-creation
-- paths (internal/adapters/inbound/httpapi's createTurnLocked/
-- CreateSessionOnTx) with the commit SHA that turn's own pre-fetched
-- diff was anchored to. See that migration's own doc comment for the
-- full "why".
--
-- answer_only (migrations/000074_plan_followup.up.sql, Step 64, §23.2)
-- mirrors review_head_sha's own identical shape one column further --
-- nil/absent for every existing call site (every CreateTurnParams
-- literal that predates this Step), set exactly once, at creation, by
-- createTurnLocked's own plan_followup gate (turn.go). See that
-- migration's own doc comment for the full "why NULL vs FALSE" split.
--
-- review_depth (migrations/000080_turns_review_depth.up.sql, Step 68,
-- §26.3) mirrors review_head_sha's own identical shape one column
-- further -- nil/absent for every non-review turn, set exactly once, at
-- creation, by every review-turn-creation path.
INSERT INTO turns (session_id, status, prompt, model_id, plan_mode, effort, review_head_sha, answer_only, review_depth)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetTurn :one
SELECT * FROM turns
WHERE id = $1;

-- name: UpdateTurnStatus :one
-- Sets a turn's status, plus dispatched_at/completed_at/
-- dispatched_sandbox_gen when the caller supplies one (sqlc.narg +
-- COALESCE: an absent/NULL argument leaves the existing column value
-- untouched, matching dispatched_at/completed_at's own nullability --
-- each is set at most once, at the (from, trigger) transition that
-- reaches Dispatched or a terminal state respectively).
--
-- dispatched_sandbox_gen (migration 000026_turn_dispatch_gen.up.sql, Step
-- 28 "turn recovery") is stamped by TWO distinct call sites sharing this
-- SAME query, both at the moment a Prompt payload is built and about to be
-- sent: tryPlanDispatch (internal/app/sessionactor/dispatch.go), alongside
-- the SAME status=dispatched write that already sets dispatched_at, for
-- the normal Pending->Dispatched->Processing path; and tryPlanReenqueue
-- (same file), which passes the CURRENT status back unchanged (the turn
-- is already, validly, Processing -- this call re-stamps
-- dispatched_sandbox_gen only, never re-transitions status) for a
-- Processing turn whose prompt needs re-sending to a respawned sandbox
-- incarnation.
UPDATE turns
SET status = $2,
    dispatched_at = COALESCE(sqlc.narg('dispatched_at'), dispatched_at),
    completed_at = COALESCE(sqlc.narg('completed_at'), completed_at),
    dispatched_sandbox_gen = COALESCE(sqlc.narg('dispatched_sandbox_gen'), dispatched_sandbox_gen)
WHERE id = $1
RETURNING *;

-- name: ListTurnsForSession :many
-- Full turn history for one session, oldest first -- exactly the input
-- shape internal/domain/session.DeriveStatus requires (an ordered slice
-- of turn.Summary derived from these rows).
SELECT * FROM turns
WHERE session_id = $1
ORDER BY created_at ASC;

-- name: MarkTurnProgressNotified :execrows
-- Audit finding M16 ("completeness", internal/adapters/outbound/linearapi/
-- doc.go): atomic, race-safe "has this turn already had its one mid-turn
-- progress milestone fired" guard -- mirrors ApprovePlanIfAwaitingApproval/
-- RejectPlanIfAwaitingApproval's own "guarded UPDATE, observed via
-- :execrows" idiom exactly (queries/plans.sql), just for a nullable
-- timestamp rather than an enum status. 0 rows affected means
-- progress_notified_at was already set for this turn (a second, later
-- tool_call event in the same turn -- the expected, common case once the
-- milestone has already fired once -- or a race); exactly 1 row affected
-- means THIS call is the one that gets to enqueue the Linear progress
-- notification (see internal/app/sessionactor/progressnotify.go).
UPDATE turns
SET progress_notified_at = $2
WHERE id = $1 AND progress_notified_at IS NULL;

-- name: GetProcessingTurnForSession :one
-- Step 61 ("builder epistemic pre-action check", §20.2) own epistemic-
-- outcome-posting endpoint's first read -- mirrors WorkflowStore's own
-- GetRunningRunForSession/GetLiveStepRunForRun precedent (queries/
-- workflows.sql): the caller (a sandbox-authenticated POST naming no turn
-- id at all, exactly like the workflow-step-outcome endpoint) resolves
-- "the session's own CURRENTLY live turn" itself, from the sandbox-
-- authenticated session id alone. turns_one_processing_per_session
-- (migrations/000005_turns.up.sql) guarantees at most one row can ever
-- match.
SELECT * FROM turns
WHERE session_id = $1 AND status = 'processing';

-- name: SetTurnEpistemicOutcome :execrows
-- The guarded UPDATE backing that same endpoint (§20.2) -- mirrors
-- SetWorkflowStepRunOutcome's own "WHERE ... AND status = 'running'" guard
-- exactly (queries/workflows.sql), one status value over: re-checks the
-- turn is STILL the live processing one at write time, closing the race
-- where it completed/failed/was cancelled between this endpoint's own
-- GetProcessingTurnForSession read and this write. Unguarded by "AND
-- epistemic_outcome IS NULL" -- deliberately, mirroring
-- SetWorkflowStepRunOutcome's own identical choice: an agent that calls
-- this endpoint more than once for the same still-processing turn (e.g.
-- correcting itself) gets last-write-wins, not a rejected second call.
-- 0 rows affected means the turn is no longer processing (a genuine race,
-- or a stale/foreign turn id having somehow been targeted -- this query
-- takes none, so in practice only the race).
UPDATE turns
SET epistemic_outcome = $2
WHERE id = $1 AND status = 'processing';
