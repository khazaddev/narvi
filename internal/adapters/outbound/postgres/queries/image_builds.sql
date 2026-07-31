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
--
-- "AND permanently_failed = false" (audit-remediation batch B3 round 2,
-- migrations/000042_image_builds_permanent_failure.up.sql) excludes a row
-- whose own repo url names a source-control host this deployment's
-- SourceControl adapter can never resolve against, no matter how many
-- times it is retried -- see that migration's own doc comment for the
-- full "why a boolean, not a new status" rationale, and RecordImageBuildPermanentFailure
-- below for how a row gets marked this way. A 'pending' row is never
-- permanently_failed by construction (UpsertPendingImageBuild never sets
-- it), but the guard is applied to both arms uniformly rather than
-- special-casing which one can never be true today.
SELECT * FROM image_builds
WHERE permanently_failed = false
  AND (status = 'pending' OR (status = 'failed' AND next_retry_at <= now()))
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

-- name: RecordImageBuildPermanentFailure :one
-- Audit-remediation batch B3 round 2 (finding #3): the TERMINAL sibling of
-- RecordImageBuildFailure above, for the one class of failure no backoff
-- schedule can ever clear -- a repo url naming a source-control host this
-- deployment's SourceControl adapter can never resolve against
-- (reposource.UnsupportedRepoHostError, surfaced via
-- imagebuild.Builder.resolveRepoSHAs). status stays 'failed' (see
-- migrations/000042_image_builds_permanent_failure.up.sql's own doc
-- comment for why this is a boolean, not a new enum value) but
-- permanently_failed flips to true and next_retry_at is cleared to NULL --
-- ListDueImageBuilds' own "AND permanently_failed = false" guard then
-- excludes this row from every future tick, on every pod, until an
-- operator fixes the session's own repo config and manually clears this
-- column (there is deliberately no automated path back). Same
-- "AND status = 'building'" guard as RecordImageBuildFailure, for the
-- identical reason (a stale/already-superseded row is a harmless no-op).
UPDATE image_builds
SET status = 'failed', permanently_failed = true, next_retry_at = NULL, updated_at = now()
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
-- load).
--
-- "refresh_in_progress = false OR refresh_started_at < @stale_claim_cutoff"
-- (audit-remediation batch B2) excludes a row another pod is ACTIVELY,
-- genuinely refreshing (matching ClaimImageBuildForRefresh's own identical
-- precondition below -- a correctness finding: this poll used to return
-- such rows too, burning a wasted resolveRepoSHAs/GitHub-API round trip
-- per pod per tick only to lose the claim) while STILL returning a row
-- whose claim has gone stale (older than
-- platform.Timeouts.ImageRefreshClaimStaleAfter), so a wedged claim left
-- by a crash between ClaimImageBuildForRefresh and RecordImageRefreshSuccess/
-- RecordImageRefreshFailure is still surfaced here for reclaiming, rather
-- than becoming permanently invisible to every future tick.
--
-- ORDER BY updated_at (same column ListDueImageBuilds orders by) gives
-- ACROSS-TICK fairness under one invariant this whole mechanism depends
-- on: attemptRefresh (app/imagebuild/builder.go) advances THIS column on
-- EVERY row it inspects this tick -- not merely ones that reach a real
-- BuildImage call -- so that ORDER BY updated_at is a genuine round-robin
-- over the WHOLE 'ready' population this query can return, never merely
-- the subset that happens to need a rebuild this tick. See attemptRefresh's
-- own top doc comment for the full starvation this rules out and why the
-- invariant is stated this way (as an invariant, not as an enumerated
-- branch count -- the exact branch count has already changed more than
-- once and prose that names a number silently rots the moment it does).
SELECT * FROM image_builds
WHERE status = 'ready' AND repo_urls != '{}'::jsonb
  AND (refresh_in_progress = false OR refresh_started_at < sqlc.arg('stale_claim_cutoff'))
ORDER BY updated_at
LIMIT $1;

-- name: ClaimImageBuildForRefresh :one
-- The freshness pump's own single-flight claim (§19.2): a CAS entirely
-- independent of status/attempt_count/next_retry_at -- it flips
-- refresh_in_progress to true (and stamps refresh_started_at with the
-- moment of THIS claim) when the row is still 'ready' AND EITHER not
-- already being refreshed by a concurrent tick, OR its existing claim has
-- gone stale (audit-remediation batch B2: refresh_started_at older than
-- @stale_claim_cutoff, i.e. platform.Timeouts.ImageRefreshClaimStaleAfter
-- ago) -- see migrations/000041_image_builds_refresh_lease.up.sql's own
-- doc comment for why this is a LEASE, not a boot-time sweep, and why that
-- distinction matters in a multi-pod deployment. status stays 'ready'
-- throughout -- see migrations/000040_image_builds_refresh_pump.up.sql's
-- own doc comment for why this must never touch status the way
-- ClaimImageBuild does for a brand-new pending/failed row: a NEW spawn's
-- own GetImageBuild lookup must keep seeing status='ready' (and the OLD
-- image_ref) for the entire window a refresh build runs. :one on zero
-- matched rows surfaces pgx.ErrNoRows -- a normal, expected "someone else
-- already claimed this one, and their claim is still fresh" outcome, not
-- an error condition.
UPDATE image_builds
SET refresh_in_progress = true, refresh_started_at = now(), updated_at = now()
WHERE fingerprint = $1
  AND status = 'ready'
  AND (refresh_in_progress = false OR refresh_started_at < sqlc.arg('stale_claim_cutoff'))
RETURNING *;

-- name: RecordImageRefreshSuccess :one
-- The freshness pump's own success half (§19.2): a SINGLE UPDATE
-- atomically swaps image_ref + built_repo_shas + built_at (never a
-- delete-then-insert) and releases the refresh_in_progress claim
-- (refresh_started_at cleared back to NULL alongside it, audit-remediation
-- batch B2) -- status stays 'ready' the whole time, so a session mid-spawn
-- never sees a gap where this fingerprint has no usable ready image_ref:
-- it reads either the OLD ref (before this commits) or the NEW one
-- (after), never neither. Guarded by "AND status = 'ready'" (a refresh can
-- never observe anything else, by construction of
-- ClaimImageBuildForRefresh above, but guarded defensively here too,
-- matching RecordImageBuildSuccess's own guard-even-though-the-caller-
-- already-checked convention) -- next_retry_at is deliberately left
-- untouched (unlike RecordImageBuildSuccess, which clears it): a refresh
-- never affects the ordinary pending/failed/backoff lifecycle those
-- columns track. The caller MUST treat a pgx.ErrNoRows/other error from
-- this query exactly like a BuildImage failure -- i.e. still release the
-- claim (RecordImageRefreshFailure) -- rather than returning early and
-- leaving refresh_in_progress wedged true (the root defect audit batch B2
-- closes; see app/imagebuild/builder.go's own attemptRefresh doc comment).
--
-- "AND refresh_started_at = @claimed_refresh_started_at" (audit-remediation
-- batch B2 round 2 -- a fencing token, closing a SECOND, separate defect
-- the "AND status = 'ready'" guard alone does not: status never changes
-- across a reclaim, so that guard alone cannot tell "I still hold the
-- lease I originally took" from "someone else's tick has since reclaimed
-- this row's lease and is now legitimately, actively refreshing it". The
-- caller passes the EXACT refresh_started_at value ClaimImageBuildForRefresh
-- returned to IT, at the moment IT took its own claim -- never a freshly
-- computed now(). If this fingerprint's lease has since gone stale and been
-- reclaimed by a concurrent tick (this pod's or another pod's), that
-- reclaim stamped a NEW refresh_started_at, so this equality fails, zero
-- rows match, and this call becomes the exact same harmless, expected
-- no-op a lost-claim-race already is elsewhere in this file -- rather than
-- unconditionally overwriting whatever the reclaiming tick has since
-- written (a fresher build silently clobbered by a stale one) or, worse,
-- wiping out that tick's own still-legitimately-held claim out from under
-- it. See app/imagebuild/builder.go's own attemptRefresh doc comment and
-- migrations/000041_image_builds_refresh_lease.up.sql's own doc comment for
-- the full failure mode this closes (a lease alone bounds how long a stale
-- claim survives; it does NOT, by itself, stop a delayed writer whose own
-- outcome-recording call outlives that bound from clobbering whoever holds
-- the lease by the time that write finally lands).
UPDATE image_builds
SET image_ref = $2, built_repo_shas = $3, built_at = $4, refresh_in_progress = false, refresh_started_at = NULL, updated_at = now()
WHERE fingerprint = $1 AND status = 'ready' AND refresh_started_at = sqlc.arg('claimed_refresh_started_at')
RETURNING *;

-- name: RecordImageRefreshFailure :one
-- The freshness pump's own failure half (§19.2): releases the
-- refresh_in_progress claim (and clears refresh_started_at back to NULL,
-- audit-remediation batch B2), touching NOTHING else -- a failed refresh
-- attempt leaves the row exactly as it was (still 'ready', still serving
-- its own old, perfectly-good image_ref/built_repo_shas/built_at), picked
-- up again at the next ImageRefreshCheckInterval tick. This is the
-- refresh path's own natural retry cadence -- no separate backoff
-- schedule is needed the way the pending/failed lifecycle's own
-- next_retry_at provides one. This is app/imagebuild.Builder's own SHARED
-- release path -- releaseRefreshClaim calls this from EVERY one of
-- attemptRefresh's own post-claim failure branches (BuildImage failure, a
-- marshal failure, and a RecordImageRefreshSuccess failure), by
-- INVARIANT, not merely the two branches this comment named on the day it
-- was first written.
--
-- "AND refresh_started_at = @claimed_refresh_started_at" (audit-remediation
-- batch B2 round 2) is a FENCING-TOKEN guard, for the identical reason
-- RecordImageRefreshSuccess above now carries one -- this query used to
-- have NO guard of any kind, meaning ANY caller holding ANY stale
-- reference to this fingerprint (e.g. a delayed writer whose claim was
-- reclaimed out from under it while this very release call was itself
-- blocked, mid-flight, on this row's lock) would unconditionally release
-- whatever claim is CURRENTLY held, including a different tick's own
-- still-legitimate, still in-flight one. The caller passes the exact
-- refresh_started_at value it read at claim time; a mismatch (the row's
-- lease has since been reclaimed) makes this call the same harmless,
-- expected no-op every other lost-claim-race in this file already is.
--
-- Deliberately NOT also guarded by "AND status = 'ready'" (unlike
-- RecordImageRefreshSuccess above) -- that omission is LOAD-BEARING, not
-- an oversight: this release path exists precisely so a claim still gets
-- released even when status has drifted away from 'ready' through some
-- OTHER, unrelated race (RecordImageRefreshSuccess's own doc comment's
-- "should-be-rare, benign race" -- see
-- TestAttemptRefresh_RecordRefreshSuccessNoOp_ReleasesClaim, which
-- exercises exactly this: status flips to 'building' out from under an
-- in-flight refresh, and the release must STILL happen). Adding a status
-- guard here would silently reintroduce a wedged refresh_in_progress claim
-- for every one of those cases -- the fencing token alone is the correct,
-- narrower fix: it rejects a release only when THIS claim instance has
-- specifically been superseded by a reclaim (refresh_started_at changed),
-- which is orthogonal to whatever status happens to be.
UPDATE image_builds
SET refresh_in_progress = false, refresh_started_at = NULL, updated_at = now()
WHERE fingerprint = $1 AND refresh_started_at = sqlc.arg('claimed_refresh_started_at')
RETURNING *;

-- name: TouchImageBuildChecked :exec
-- The freshness pump's own genuine-round-robin bookkeeping (§19.2, fixing
-- a correctness review finding on the batch-cap fix: see
-- ListReadyImageBuilds' own doc comment above for the full starvation
-- mechanism this closes). Bumps ONLY updated_at for fingerprint --
-- status/image_ref/built_repo_shas/built_at/attempt_count/next_retry_at/
-- refresh_in_progress/refresh_started_at are every one of them left
-- completely untouched. This is deliberately NOT a state transition of any
-- kind -- it is purely "attemptRefresh (app/imagebuild/builder.go)
-- INSPECTED this row this tick". attemptRefresh calls this from EVERY one
-- of its own early-return branches that does not otherwise advance
-- updated_at some other way (a decode failure, the base-only guard, a
-- resolveRepoSHAs error, NeedsRefresh reporting still-fresh, and a lost
-- ClaimImageBuildForRefresh race/error) -- an INVARIANT ("every inspected
-- row's ordering key advances, one way or another, before attemptRefresh
-- returns"), not a fixed enumerated list: the exact set of branches has
-- already grown more than once, and prose naming a branch count silently
-- rots the next time it does.
--
-- "AND status = 'ready'" is a defensive guard, not a load-bearing one (by
-- construction, attemptRefresh only ever calls this for a row
-- ListReadyImageBuilds just returned, which is 'ready' by that query's own
-- WHERE clause) -- it exists purely so this fire-and-forget call can never
-- perturb ListDueImageBuilds' own, textually identical "ORDER BY
-- updated_at" fairness ordering for the SEPARATE pending/failed lifecycle,
-- even under a future bug that calls this for a non-'ready' fingerprint.
--
-- :exec, not :one: this is fire-and-forget bookkeeping, never a CAS --
-- a fingerprint no longer 'ready' (or gone entirely, a should-be-rare
-- race) is a silent, harmless no-op, not an error condition worth a
-- caller check the way a real claim's lost-race outcome is.
UPDATE image_builds
SET updated_at = now()
WHERE fingerprint = $1 AND status = 'ready';
