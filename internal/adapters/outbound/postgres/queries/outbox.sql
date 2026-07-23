-- Queries backing Outbox (§4.3, §5.1). CreateOutboxEntry/GetOutboxEntry
-- prove the pipeline end to end (Step 31); the remaining five back Step
-- 35's ("outbox delivery") own claim/attempt/record delivery-worker loop
-- (internal/app/outboxworker), mirroring internal/adapters/outbound/
-- postgres/queries/image_builds.sql's own ListDue/Claim/RecordSuccess/
-- RecordFailure shape closely -- see that file's own doc comments for the
-- general "claim inside one transaction, attempt outside any transaction"
-- discipline this mirrors.
--
-- Unlike image_builds, the outbox table has no third, in-flight status
-- distinct from pending/delivered/dead_letter (migrations/000010_outbox.
-- up.sql's own outbox_status enum is exactly pending/delivered/
-- dead_letter) -- so ClaimOutboxEntry below cannot flip status the way
-- ClaimImageBuild flips to 'building'. Instead it bumps next_attempt_at
-- forward by the caller's own claim-protection window (platform.Timeouts.
-- OutboxClaimDuration), mirroring app/sessionactor/timerpump.go's own
-- ClaimDueTimer precedent exactly -- see that query's own doc comment for
-- why a provisional forward-bump at claim time is the correct mechanism
-- here.

-- name: CreateOutboxEntry :one
INSERT INTO outbox (session_id, kind, payload)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetOutboxEntry :one
SELECT * FROM outbox
WHERE id = $1;

-- name: ListDuePendingOutboxEntries :many
-- outboxworker.Builder's own poll query: every 'pending' row whose own
-- next_attempt_at has elapsed, oldest-due first. FOR UPDATE SKIP LOCKED
-- mirrors ListDueImageBuilds/ListDueTimers' own identical precedent --
-- multiple control-plane pods may run this same background loop
-- independently; SKIP LOCKED lets two concurrent pods each claim a
-- DISJOINT batch instead of blocking on each other or double-claiming the
-- same row.
SELECT * FROM outbox
WHERE status = 'pending' AND next_attempt_at <= now()
ORDER BY next_attempt_at
LIMIT $1
FOR UPDATE SKIP LOCKED;

-- name: ClaimOutboxEntry :one
-- The claim half of the pump's own two-step (claim-then-attempt-outside-
-- any-transaction) shape: bumps next_attempt_at forward by the caller's
-- own OutboxClaimDuration protection window and increments attempts,
-- committed BEFORE the real notifier call is ever attempted -- mirrors
-- ClaimImageBuild's own "attempt_count counts ATTEMPTS made, not merely
-- failures" convention exactly, so domain/outbox.EvaluateBackoff is later
-- asked to schedule (or dead-letter) THIS attempt using the post-increment
-- count. Guarded by "AND status = 'pending'" so a stale/already-superseded
-- row (should be impossible given ListDuePendingOutboxEntries' own WHERE
-- clause, but defensive, mirroring RecordImageBuildSuccess/Failure's own
-- identical guard) is a harmless no-op.
UPDATE outbox
SET attempts = attempts + 1, next_attempt_at = $2
WHERE id = $1 AND status = 'pending'
RETURNING *;

-- name: MarkOutboxEntryDelivered :one
-- Records a successful delivery: status='delivered', delivered_at=now().
-- Guarded by "AND status = 'pending'", mirroring RecordImageBuildSuccess's
-- own identical guard against a stale/already-superseded row.
UPDATE outbox
SET status = 'delivered', delivered_at = now()
WHERE id = $1 AND status = 'pending'
RETURNING *;

-- name: RecordOutboxEntryFailure :one
-- Records a failed delivery attempt that is still eligible for another
-- retry: next_attempt_at is the caller's own domain/outbox.EvaluateBackoff-
-- computed value (overwriting ClaimOutboxEntry's own provisional bump
-- above with the real decision), last_error captures the notifier's own
-- error for observability. attempts is NOT incremented again here --
-- ClaimOutboxEntry already counted this attempt. Same "AND status =
-- 'pending'" guard as MarkOutboxEntryDelivered, for the identical reason.
UPDATE outbox
SET next_attempt_at = $2, last_error = $3
WHERE id = $1 AND status = 'pending'
RETURNING *;

-- name: MarkOutboxEntryDeadLetter :one
-- Records a failed delivery attempt that has exhausted domain/outbox.
-- MaxAttempts: status='dead_letter', last_error captures the notifier's
-- own final error. Same "AND status = 'pending'" guard as
-- MarkOutboxEntryDelivered/RecordOutboxEntryFailure above.
UPDATE outbox
SET status = 'dead_letter', last_error = $2
WHERE id = $1 AND status = 'pending'
RETURNING *;
