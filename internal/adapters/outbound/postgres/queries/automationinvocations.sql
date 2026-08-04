-- Queries backing AutomationInvocationStore (Step 51, "automations:
-- engine", §3.5), migrations/000052_automation_invocations.up.sql.

-- name: CreateAutomationInvocation :one
-- Fast, cheap, durable hand-off (mirrors internal/app/releasereview.
-- Enqueue's own identical "one INSERT, the real work happens later on a
-- background loop's own schedule" shape) -- the caller (this Step: tests
-- only; Step 52: a real trigger-condition evaluation) has already run
-- automation.ValidateTargets against targets before calling this.
INSERT INTO automation_invocations (automation_id, targets, total_runs)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetAutomationInvocation :one
SELECT * FROM automation_invocations
WHERE id = $1;

-- name: ListDueForFanOut :many
-- Every invocation not yet claimed for fan-out, oldest first, locked FOR
-- UPDATE SKIP LOCKED -- callers MUST run this inside the same transaction
-- that subsequently calls ClaimAutomationInvocationForFanOut on each
-- returned row, mirroring ListDueImageBuilds/ListDuePendingOutbox's own
-- identical claim-batch precedent exactly.
SELECT * FROM automation_invocations
WHERE fanned_out_at IS NULL
ORDER BY created_at
LIMIT $1
FOR UPDATE SKIP LOCKED;

-- name: ClaimAutomationInvocationForFanOut :one
-- The CAS half of the claim-batch pair immediately above -- "UPDATE ...
-- WHERE fanned_out_at IS NULL" guards against a crash-and-retry
-- double-fanning-out the SAME invocation (see this table's own migration
-- doc comment).
UPDATE automation_invocations
SET fanned_out_at = now()
WHERE id = $1 AND fanned_out_at IS NULL
RETURNING *;

-- name: CloseAutomationInvocation :one
-- Applies internal/domain/automation.InvocationTransition's own verdict --
-- guarded by "AND status = 'pending'" so this invocation's own outcome is
-- decided at most once, no matter how many concurrent callers (this
-- Step's reconcile pump, the recovery sweep, or -- in principle -- both,
-- racing on the SAME invocation's last remaining run) observe "every run
-- is now terminal" at roughly the same time; exactly one of them ever
-- wins this UPDATE (returns pgx.ErrNoRows to every loser, app/automation's
-- own closeout.go).
UPDATE automation_invocations
SET status = $2,
    closed_at = now()
WHERE id = $1 AND status = 'pending'
RETURNING *;

-- name: MarkAutomationInvocationFailureCounted :one
-- §3.5's own literal CAS idiom: "UPDATE ... WHERE failure_counted_at IS
-- NULL" -- called only for an invocation CloseAutomationInvocation just
-- decided is Failed, in the SAME transaction as
-- LockAutomationForUpdate/ApplyFailureStrike (queries/automations.sql),
-- guarding the failure-strike CONSEQUENCE against being applied twice
-- even if this invocation's own close-out is somehow re-attempted after a
-- crash between the two (internal/domain/automation/doc.go's own "two
-- independent CAS guards, not one" section).
UPDATE automation_invocations
SET failure_counted_at = now()
WHERE id = $1 AND failure_counted_at IS NULL
RETURNING *;
