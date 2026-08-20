-- Queries backing DigestSendStateStore (§21.3) -- see
-- migrations/000071_digest_send_state.up.sql's own doc comment for the
-- full two-phase (seed, then claim) at-most-one-send design.

-- name: SeedDigestSendState :one
-- Phase 1 (idempotent seed): ensures a 'pending' row exists for
-- (sendDate, channelProvider, channelID) -- ON CONFLICT DO NOTHING means
-- a channel/date pair already seeded (by an earlier tick, or a
-- concurrent one) is a silent no-op; returns no row in that case
-- (:one still reports pgx.ErrNoRows, which the caller treats as "already
-- seeded", never an error).
INSERT INTO digest_send_state (send_date, channel_provider, channel_id)
VALUES ($1, $2, $3)
ON CONFLICT (send_date, channel_provider, channel_id) DO NOTHING
RETURNING *;

-- name: ClaimPendingDigestSendState :many
-- Phase 2 (claim): atomically claims up to $2 still-'pending' rows for
-- sendDate, oldest-created-first, via SELECT ... FOR UPDATE SKIP LOCKED
-- + an UPDATE to 'sending' in the SAME statement (a single round trip,
-- exactly like queries/releasemanifestpending.sql's own identical
-- DELETE-via-SKIP-LOCKED-subquery shape, adapted here to an UPDATE since
-- a digest's own send_state row must SURVIVE the claim, unlike a
-- release-manifest-pending row, which is one-shot and has nothing further
-- to record against it afterward). Two concurrent internal/app/digest.Pump
-- ticks (different pods, or an overlapping tick) calling this
-- simultaneously each claim a DISJOINT set of rows -- SKIP LOCKED means
-- the second tick's own scan silently passes over whatever the first
-- tick's SELECT ... FOR UPDATE is still holding a row lock on, rather
-- than blocking on it or double-claiming it.
UPDATE digest_send_state AS outer_row
SET status = 'sending', claimed_at = now()
WHERE outer_row.id IN (
    SELECT inner_row.id FROM digest_send_state AS inner_row
    WHERE inner_row.send_date = $1 AND inner_row.status = 'pending'
    ORDER BY inner_row.created_at
    LIMIT $2
    FOR UPDATE SKIP LOCKED
)
RETURNING outer_row.*;

-- name: MarkDigestSendStateSent :exec
-- Called after the digest's own outbox entry has been successfully
-- CREATED (never after real delivery -- delivery itself is the outbox
-- worker's own, separately-retried job, §5.1's outbox pattern) --
-- 'sent' here means "handed off to the durable outbox", the same
-- boundary every other write-then-enqueue call site in this codebase
-- (e.g. reviewverdict.go) treats as its own success point.
UPDATE digest_send_state
SET status = 'sent', sent_at = now()
WHERE id = $1;

-- name: MarkDigestSendStateFailed :exec
-- Called when building/enqueuing a claimed row's own digest failed
-- (e.g. the rollup query itself errored) -- 'failed' is a TERMINAL state
-- for that row (mirrors outbox's own dead_letter: no retry loop
-- re-claims a 'failed' row for the SAME send_date, since "one digest, up
-- to once, per channel per day" is the property this table exists to
-- guarantee -- a failed digest is surfaced via this row's own
-- last_error, and control-plane logs/alerts, not silently retried into
-- a possible double-send later the same day).
UPDATE digest_send_state
SET status = 'failed', last_error = $2
WHERE id = $1;
