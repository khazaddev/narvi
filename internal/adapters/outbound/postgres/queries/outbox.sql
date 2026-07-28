-- Queries backing Outbox (§4.3, §5.1). CreateOutboxEntry/GetOutboxEntry
-- prove the pipeline end to end (Step 31); the next five back Step 35's
-- ("outbox delivery") own claim/attempt/record delivery-worker loop
-- (internal/app/outboxworker), mirroring internal/adapters/outbound/
-- postgres/queries/image_builds.sql's own ListDue/Claim/RecordSuccess/
-- RecordFailure shape closely -- see that file's own doc comments for the
-- general "claim inside one transaction, attempt outside any transaction"
-- discipline this mirrors. RenewOutboxClaim/CountPendingOutboxEntries at
-- the bottom are a later audit fix (H6/M15-M17) -- see each query's own
-- doc comment below.
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
-- correlation_id (migrations/000037_outbox_correlation_id.up.sql) is
-- nullable, mirroring audit_log.correlation_id's own identical
-- convention: every caller (internal/app/sessionactor/outboxenqueue.go's
-- own enqueueOutboxNotification; internal/adapters/inbound/httpapi/
-- decideplan.go's own enqueuePlanDecisionNotifications) passes
-- platform.CorrelationIDFromContext(ctx)'s value when the enclosing
-- request/webhook context carried one, else NULL -- no id is ever invented
-- at enqueue time for a row created outside such a context.
INSERT INTO outbox (session_id, kind, payload, correlation_id)
VALUES ($1, $2, $3, $4)
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

-- name: RenewOutboxClaim :one
-- Audit fix (H6, correctness -- internal/app/outboxworker/builder.go): the
-- per-row re-claim/heartbeat outboxworker.Builder's own attempt() calls
-- immediately before the real notifier.Deliver call, closing the batch-
-- level claim-lease race ClaimOutboxEntry above cannot close on its own.
-- claimBatch computes ONE shared time.Now() and calls ClaimOutboxEntry for
-- EVERY row in a batch (up to pumpBatchSize) before any of them are
-- actually delivered; PumpOnce then attempts each claimed row
-- SEQUENTIALLY, so a row late in the batch could still be waiting for its
-- own attempt() call to even start after its shared claim-expiry
-- timestamp has already elapsed -- letting a concurrent tick (this pod's
-- next one, or another pod's own Builder) re-claim and redeliver the same
-- row while the first delivery is still in flight.
--
-- This query renews ONLY the ONE row about to be delivered, bumping
-- next_attempt_at forward by the caller's own OutboxClaimDuration from a
-- FRESH time.Now() taken at the moment of the real attempt -- not the
-- batch's shared claim-time now. Deliberately does NOT touch attempts:
-- ClaimOutboxEntry already counted this attempt at batch-claim time, and
-- this is purely a claim-protection renewal, not a new delivery attempt.
-- Same "AND status = 'pending'" guard as every other guarded outbox
-- UPDATE above -- a no-op if the row is no longer pending.
UPDATE outbox
SET next_attempt_at = $2
WHERE id = $1 AND status = 'pending'
RETURNING *;

-- name: CountPendingOutboxEntries :one
-- Audit fix (M15/M17, the lag-metric blind spot): outboxworker.Builder's
-- own outbox_lag_seconds gauge is computed ONLY from rows the CURRENT
-- tick's own claimBatch actually claimed (ListDuePendingOutboxEntries
-- above, which only returns rows whose own next_attempt_at <= now()) --
-- during a sustained notifier outage, every failed delivery schedules a
-- real domain/outbox.EvaluateBackoff-computed retry, so at any given tick
-- it is entirely possible every currently-pending row is mid-backoff (its
-- own next_attempt_at in the future, not due yet) even though there is a
-- large, genuinely stuck backlog -- in that moment outbox_lag_seconds
-- silently reads zero. This query backs a SECOND, independent gauge
-- (outbox_due_backlog_count) that counts EVERY 'pending' row, deliberately
-- NOT restricted to next_attempt_at <= now() like ListDuePendingOutboxEntries
-- -- the whole point is to see rows currently cooling down in backoff too,
-- which the due-only lag metric structurally cannot see. Deliberately
-- outside claimBatch's own transaction: a cheap, standalone read, no FOR
-- UPDATE, no lock contention with the claim step.
SELECT COUNT(*) FROM outbox
WHERE status = 'pending';
