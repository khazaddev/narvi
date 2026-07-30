-- Queries backing ImageBuildStore (Step 26, "image builds", §8.5-note/
-- §10-P2; Step 41, "warm boot: shared fingerprint", §19.1). See
-- migrations/000024_image_builds.up.sql and
-- migrations/000039_image_builds_shared_fingerprint.up.sql for the
-- table's own doc comments (why base/repo_urls/runtime_version are
-- persisted alongside the fingerprint, not just the hash; why repo_urls
-- carries each repo's normalized clone URL rather than a resolved SHA;
-- why built_repo_shas/built_at exist).

-- name: GetImageBuild :one
-- dispatch.go's own spawn-time lookup: status='ready' -> use image_ref;
-- anything else (or pgx.ErrNoRows) -> the caller falls back to the base
-- image for THIS spawn, never blocking on a build (§10 Phase 2).
SELECT * FROM image_builds
WHERE fingerprint = $1;

-- name: UpsertPendingImageBuild :exec
-- Best-effort tracking-row creation, called from imageresolve.go on ANY
-- miss (no row yet) -- ON CONFLICT DO NOTHING is correct, not merely
-- convenient: a fingerprint deterministically encodes
-- (base, repo_urls, runtime_version), so a row already existing under the
-- SAME fingerprint already carries identical values for those columns
-- (barring an astronomically unlikely hash collision) -- there is nothing
-- to update, and this must never clobber an existing row's own
-- status/attempt_count/next_retry_at bookkeeping the background builder
-- (internal/app/imagebuild) owns exclusively. :exec (not :one) because a
-- conflict correctly returns zero rows, which a :one query would
-- surface as pgx.ErrNoRows -- the caller has no use for the row anyway.
INSERT INTO image_builds (fingerprint, base, repo_urls, runtime_version)
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
-- built_repo_shas/built_at (§19.1) record the CONCRETE per-repo SHAs this
-- specific successful build actually used (and when) -- the caller's own
-- ports.ImageSpec.Repos, not repo_urls' own URL-keyed fingerprint input --
-- so §19.2's later freshness pump has something to compare a repo's
-- current default-branch tip against.
UPDATE image_builds
SET status = 'ready', image_ref = $2, built_repo_shas = $3, built_at = $4, next_retry_at = NULL, updated_at = now()
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

-- name: ListReadyImageBuilds :many
-- Step 42's own freshness-pump poll query (§19.2): every SHARED (repo-
-- bearing) 'ready' row -- a base-only row (repo_urls = '{}') is never
-- stale in the sense this design cares about (there is no repo tip to
-- drift from), so it is excluded here at the SQL level rather than making
-- every caller re-check len(repoUrls) == 0 itself. Plain SELECT, no
-- row-level locking: the freshness pump's own single-flight protection is
-- ClaimImageBuildForRefresh's own per-row CAS below, not a batch-level
-- FOR UPDATE SKIP LOCKED claim -- resolving each repo's current
-- default-branch tip (a real GitHub API call per repo) happens OUTSIDE
-- any transaction, so holding a batch-level lock across that network work
-- would be exactly the "a real network call must never hold a Postgres
-- transaction open" mistake this codebase's own established discipline
-- (app/sessionactor/dispatch.go, app/imagebuild.Builder.claimBatch) exists
-- to avoid.
--
-- LIMIT $1 mirrors ListDueImageBuilds' own batch-cap shape exactly (a
-- correctness/scalability review finding on this Step: an unbounded
-- ListReady, followed by strictly-sequential per-row attemptRefresh calls
-- -- each a real, synchronous, network-bound BuildImage call that can take
-- minutes -- let one slow/blocked build in a large batch delay even
-- STARTING every other Environment's own tip-SHA check for the rest of
-- that tick, degrading the fleet's effective refresh cadence well past
-- this section's own documented 10-40 minute staleness window under
-- load). ORDER BY updated_at (same column ListDueImageBuilds orders by)
-- gives ACROSS-TICK fairness for free: a row currently mid-refresh had its
-- own updated_at just bumped by ClaimImageBuildForRefresh, so it
-- naturally sorts toward the back of the next tick's own batch, behind
-- rows this pump hasn't touched in longer -- no separate bookkeeping
-- needed to avoid one hot fingerprint monopolizing every tick's own
-- limited batch.
SELECT * FROM image_builds
WHERE status = 'ready' AND repo_urls != '{}'::jsonb
ORDER BY updated_at
LIMIT $1;

-- name: ClaimImageBuildForRefresh :one
-- The freshness pump's own single-flight claim (§19.2): a CAS entirely
-- independent of status/attempt_count/next_retry_at -- it flips
-- refresh_in_progress to true ONLY when the row is still 'ready' AND not
-- already being refreshed by a concurrent tick (this pod's or another
-- pod's own Builder). status stays 'ready' throughout -- see
-- migrations/000040_image_builds_refresh_pump.up.sql's own doc comment
-- for why this must never touch status the way ClaimImageBuild does for
-- a brand-new pending/failed row: a NEW spawn's own GetImageBuild lookup
-- must keep seeing status='ready' (and the OLD image_ref) for the entire
-- window a refresh build runs. :one on zero matched rows surfaces
-- pgx.ErrNoRows -- a normal, expected "someone else already claimed this
-- one" outcome, not an error condition.
UPDATE image_builds
SET refresh_in_progress = true, updated_at = now()
WHERE fingerprint = $1 AND status = 'ready' AND refresh_in_progress = false
RETURNING *;

-- name: RecordImageRefreshSuccess :one
-- The freshness pump's own success half (§19.2): a SINGLE UPDATE
-- atomically swaps image_ref + built_repo_shas + built_at (never a
-- delete-then-insert) and releases the refresh_in_progress claim --
-- status stays 'ready' the whole time, so a session mid-spawn never sees
-- a gap where this fingerprint has no usable ready image_ref: it reads
-- either the OLD ref (before this commits) or the NEW one (after), never
-- neither. Guarded by "AND status = 'ready'" (a refresh can never observe
-- anything else, by construction of ClaimImageBuildForRefresh above, but
-- guarded defensively here too, matching RecordImageBuildSuccess's own
-- guard-even-though-the-caller-already-checked convention) -- next_retry_at
-- is deliberately left untouched (unlike RecordImageBuildSuccess, which
-- clears it): a refresh never affects the ordinary pending/failed/backoff
-- lifecycle those columns track.
UPDATE image_builds
SET image_ref = $2, built_repo_shas = $3, built_at = $4, refresh_in_progress = false, updated_at = now()
WHERE fingerprint = $1 AND status = 'ready'
RETURNING *;

-- name: RecordImageRefreshFailure :one
-- The freshness pump's own failure half (§19.2): simply releases the
-- refresh_in_progress claim, touching NOTHING else -- a failed refresh
-- attempt leaves the row exactly as it was (still 'ready', still serving
-- its own old, perfectly-good image_ref/built_repo_shas/built_at), picked
-- up again at the next ImageRefreshCheckInterval tick. This is the
-- refresh path's own natural retry cadence -- no separate backoff
-- schedule is needed the way the pending/failed lifecycle's own
-- next_retry_at provides one.
UPDATE image_builds
SET refresh_in_progress = false, updated_at = now()
WHERE fingerprint = $1
RETURNING *;
