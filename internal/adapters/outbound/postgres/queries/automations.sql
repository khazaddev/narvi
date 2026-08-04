-- Queries backing AutomationStore (Step 51, "automations: engine", §3.5),
-- migrations/000051_automations.up.sql.

-- name: CreateAutomation :one
-- No caller exists yet in this Step (no HTTP surface for creating/
-- configuring automations -- Step 52/76 own that, per this Step's own
-- scope note); used directly by this package's own integration tests, and
-- ready for Step 52's own admin CRUD endpoint to call unchanged.
INSERT INTO automations (name, prompt, repos, created_by)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetAutomation :one
SELECT * FROM automations
WHERE id = $1;

-- name: LockAutomationForUpdate :one
-- Row-level lock backing the failure-strike accounting step
-- (app/automation's own closeout.go): taken in the SAME transaction as
-- MarkAutomationInvocationFailureCounted (queries/automationinvocations.sql)
-- so two invocations belonging to the SAME automation that close (fail)
-- concurrently apply their own automation.EvaluateFailureStrike
-- consequence one at a time, never as a lost update racing against each
-- other's own read-then-write of consecutive_failures.
SELECT * FROM automations
WHERE id = $1
FOR UPDATE;

-- name: ApplyFailureStrike :one
-- Records automation.EvaluateFailureStrike's own verdict -- called only
-- while still holding LockAutomationForUpdate's own row lock, in the SAME
-- transaction.
UPDATE automations
SET consecutive_failures = $2,
    status = $3,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ResetConsecutiveFailures :execrows
-- A succeeded invocation resets its own automation's streak to zero --
-- idempotent (no CAS guard needed the way the failure path needs one,
-- §3.5's own "at-most-one failure strike" requirement does not apply to a
-- success: applying this reset twice is harmless, unlike double-counting
-- a failure). "AND consecutive_failures <> 0" is a pure no-op-avoidance
-- optimization (skip a write when there is nothing to reset), never a
-- correctness guard.
UPDATE automations
SET consecutive_failures = 0,
    updated_at = now()
WHERE id = $1 AND consecutive_failures <> 0;

-- name: ResumeAutomation :one
-- Backs automation.TriggerResume (internal/domain/automation) -- no HTTP
-- caller exists yet in this Step (Step 52/76 own the actual "Resume"
-- button, mockups.html's own Automations view), reserved so that surface
-- needs no store-layer change to use it. Guarded by "AND status =
-- 'paused'" so a non-paused automation's own resume attempt affects zero
-- rows rather than silently no-op-writing an already-active row.
UPDATE automations
SET status = 'active',
    consecutive_failures = 0,
    updated_at = now()
WHERE id = $1 AND status = 'paused'
RETURNING *;
