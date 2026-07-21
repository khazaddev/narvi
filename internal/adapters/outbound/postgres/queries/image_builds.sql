-- Queries backing ImageBuildStore (Step 26, "image builds", §8.5-note/
-- §10-P2). See migrations/000024_image_builds.up.sql for the table's own
-- doc comment (why base/repo_shas/runtime_version are persisted alongside
-- the fingerprint, not just the hash).

-- name: GetImageBuild :one
-- dispatch.go's own spawn-time lookup: status='ready' -> use image_ref;
-- anything else (or pgx.ErrNoRows) -> the caller falls back to the base
-- image for THIS spawn, never blocking on a build (§10 Phase 2).
SELECT * FROM image_builds
WHERE fingerprint = $1;

-- name: UpsertPendingImageBuild :exec
-- Best-effort tracking-row creation, called from dispatch.go on ANY miss
-- (no row yet) -- ON CONFLICT DO NOTHING is correct, not merely
-- convenient: a fingerprint deterministically encodes
-- (base, repo_shas, runtime_version), so a row already existing under the
-- SAME fingerprint already carries identical values for those columns
-- (barring an astronomically unlikely hash collision) -- there is nothing
-- to update, and this must never clobber an existing row's own
-- status/attempt_count/next_retry_at bookkeeping the background builder
-- (internal/app/imagebuild) owns exclusively. :exec (not :one) because a
-- conflict correctly returns zero rows, which a :one query would
-- surface as pgx.ErrNoRows -- the caller has no use for the row anyway.
INSERT INTO image_builds (fingerprint, base, repo_shas, runtime_version)
VALUES ($1, $2, $3, $4)
ON CONFLICT (fingerprint) DO NOTHING;

-- name: ListDueImageBuilds :many
-- internal/app/imagebuild's own poll query: every row eligible to
-- (re)attempt right now -- a fresh 'pending' row, or a 'failed' row whose
-- own next_retry_at (domain/imagebuild.EvaluateBackoff's own decision) has
-- elapsed. 'building' is deliberately excluded: a row already claimed by
-- some tick (this pod's or another's) is mid-attempt, not due again.
-- FOR UPDATE SKIP LOCKED mirrors app/sessionactor/timerpump.go's own
-- claimDueTimers precedent exactly -- multiple control-plane pods may run
-- this same background loop independently; SKIP LOCKED is what lets two
-- concurrent pods each claim a DISJOINT batch instead of blocking on each
-- other or double-claiming the same fingerprint.
SELECT * FROM image_builds
WHERE status = 'pending' OR (status = 'failed' AND next_retry_at <= now())
ORDER BY updated_at
LIMIT $1
FOR UPDATE SKIP LOCKED;

-- name: ClaimImageBuild :one
-- The claim half of the pump's own two-step (claim-then-attempt-outside-
-- any-transaction) shape, mirroring app/reconciler and app/sessionactor's
-- own established "a real network call (BuildImage) must never hold a
-- Postgres transaction open" discipline: flips status to 'building' and
-- bumps attempt_count/last_attempt_at, committed BEFORE the real
-- BuildImage call is ever attempted. attempt_count is incremented here,
-- unconditionally -- it counts ATTEMPTS made, not merely failures, so it
-- reflects "this is attempt N" at the moment domain/imagebuild.
-- EvaluateBackoff is later asked to schedule a retry for THIS attempt, if
-- it fails.
UPDATE image_builds
SET status = 'building', attempt_count = attempt_count + 1, last_attempt_at = now(), updated_at = now()
WHERE fingerprint = $1
RETURNING *;

-- name: RecordImageBuildSuccess :one
-- The success half of the outcome-recording step, run in a SECOND, fresh
-- transaction AFTER the real BuildImage call (outside any transaction) has
-- already returned -- mirrors app/sessionactor/dispatch.go's own
-- recordProviderOutcome precedent. Guarded by "AND status = 'building'"
-- so a stale/already-superseded row is a harmless no-op (:one on zero
-- matched rows surfaces pgx.ErrNoRows, which the caller logs and moves on
-- from -- exactly like recordProviderOutcome's own superseded-gen guard).
UPDATE image_builds
SET status = 'ready', image_ref = $2, next_retry_at = NULL, updated_at = now()
WHERE fingerprint = $1 AND status = 'building'
RETURNING *;

-- name: RecordImageBuildFailure :one
-- The failure half of the outcome-recording step: next_retry_at is the
-- caller's own domain/imagebuild.EvaluateBackoff-computed value (§3.5:
-- "not fixed 30 min" -- exponential, capped). Same "AND status = 'building'"
-- guard as RecordImageBuildSuccess above, for the identical reason.
UPDATE image_builds
SET status = 'failed', next_retry_at = $2, updated_at = now()
WHERE fingerprint = $1 AND status = 'building'
RETURNING *;
