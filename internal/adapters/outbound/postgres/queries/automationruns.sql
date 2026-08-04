-- Queries backing AutomationRunStore (Step 51, "automations: engine",
-- §3.5), migrations/000053_automation_runs.up.sql.

-- name: CreateAutomationRun :one
-- sessionID is sqlc.narg (nullable): set when this run's own
-- CreateSessionOnTx call (app/automation's own fanout.go) succeeded for
-- its target, NULL when it failed before any session ever existed (the
-- run is still created either way, RunStatusStarting or RunStatusFailed
-- respectively -- every target names exactly one row, regardless of
-- outcome, so total_runs on the parent invocation always matches the real
-- row count).
INSERT INTO automation_runs (invocation_id, automation_id, target, session_id, status)
VALUES ($1, $2, $3, sqlc.narg('session_id'), $4)
RETURNING *;

-- name: GetAutomationRun :one
SELECT * FROM automation_runs
WHERE id = $1;

-- name: ListInFlightRuns :many
-- Every run still starting/running, oldest first, bounded by limit --
-- app/automation's own reconcile pump (ReconcileOnce). A plain SELECT, not
-- FOR UPDATE SKIP LOCKED: the actual state-changing writes below
-- (TerminalizeAutomationRun/PromoteAutomationRunToRunning) are each their
-- own CAS-guarded UPDATE, so two concurrent readers observing the SAME
-- row here is harmless -- at most one of them ever wins the later write
-- (mirrors app/reconciler.ReconcileOnce's own identical "the read is
-- plain, only the write is race-guarded" reasoning for provider.List/
-- ListLiveProviderIDs).
SELECT * FROM automation_runs
WHERE status IN ('starting', 'running')
ORDER BY created_at
LIMIT $1;

-- name: PromoteAutomationRunToRunning :one
-- automation.RunTriggerProcessing: Starting -> Running. Guarded by "AND
-- status = 'starting'" -- a losing/duplicate promotion attempt (this run
-- already promoted by a concurrent reconcile tick) affects zero rows,
-- observed via pgx.ErrNoRows, never a partial/duplicate write.
UPDATE automation_runs
SET status = 'running',
    running_at = now()
WHERE id = $1 AND status = 'starting'
RETURNING *;

-- name: TerminalizeAutomationRun :one
-- Applies internal/domain/automation.RunTransition's own verdict (to is
-- always RunStatusSucceeded/RunStatusFailed) -- guarded by "AND status IN
-- ('starting', 'running')" (a non-terminal-only guard, not an exact
-- single-status match: see this file's own top doc comment on
-- ListInFlightRuns for why a status change between this run's own SELECT
-- and this UPDATE is harmless here -- the terminal status itself is
-- always freshly re-derived from the linked session's CURRENT turn
-- history, never a stale in-memory value). Used by BOTH the reconcile
-- pump (a genuine turn outcome) and the recovery sweep (an orphan
-- timeout) -- the SAME guard correctly excludes an already-terminal run
-- from either caller.
UPDATE automation_runs
SET status = $2,
    completed_at = now()
WHERE id = $1 AND status IN ('starting', 'running')
RETURNING *;

-- name: CountTerminalRunsForInvocation :one
-- Feeds automation.EvaluateInvocationOutcome (app/automation's own
-- closeout.go): how many of this invocation's own runs are terminal, and
-- how many of those specifically failed. total_runs itself is already
-- persisted on automation_invocations (no need to recompute it here).
SELECT
    count(*) FILTER (WHERE status IN ('succeeded', 'failed')) AS terminal_runs,
    count(*) FILTER (WHERE status = 'failed') AS failed_runs
FROM automation_runs
WHERE invocation_id = $1;

-- name: ListOrphanedStartingRuns :many
-- §3.5's own "orphaned starting runs >5 min" sweep -- started_at older
-- than cutoff (platform.Timeouts.AutomationRunStartingOrphanThreshold
-- subtracted from "now" by the caller, mirroring app/imagebuild.
-- RefreshOnce's own staleClaimCutoff precedent: one cutoff instant computed
-- ONCE per tick, not a fresh now() per row).
SELECT * FROM automation_runs
WHERE status = 'starting' AND started_at < $1
ORDER BY started_at
LIMIT $2;

-- name: ListOrphanedRunningRuns :many
-- §3.5's own "running >90 min" sweep -- same shape as
-- ListOrphanedStartingRuns immediately above, against running_at/its own
-- distinct cutoff.
SELECT * FROM automation_runs
WHERE status = 'running' AND running_at < $1
ORDER BY running_at
LIMIT $2;
