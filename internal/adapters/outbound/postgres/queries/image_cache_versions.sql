-- Queries backing ImageCacheVersionStore (Step 43(c), third iteration:
-- immutable versioned cache snapshots -- §19.1's closing paragraph). See
-- migrations/000087_image_cache_versions.up.sql for the two-table shape's
-- own full doc comment (why a reservation counter and a confirmed-history
-- table are separate) and ports.CacheMount (internal/app/ports/
-- createspec.go) for the port-level contract these back.

-- name: MintCacheVersion :one
-- Atomically reserves the NEXT version number for cache_key -- called by
-- app/imagebuild.Builder BEFORE every real BuildImage attempt, so the
-- reserved number can travel as that attempt's own
-- ports.CacheMount.PublishVersion. A reservation that is never confirmed
-- by PublishCacheVersion below (the build failed, or the adapter's own
-- decline-and-retry-cold fallback dropped the mount before the attempt
-- that ultimately succeeded) is simply abandoned -- a harmless gap in this
-- key's own sequence, never retried, never reused. First call for a
-- cache_key inserts next_version=1 (no conflict) and returns 1; every
-- subsequent call increments the existing row and returns the new value --
-- a standard Postgres upsert-counter, safe under concurrent claimants via
-- ordinary single-row locking (concurrent callers serialize on this one
-- row rather than racing to compute the same number independently).
INSERT INTO image_cache_version_counters (cache_key, next_version)
VALUES ($1, 1)
ON CONFLICT (cache_key) DO UPDATE SET next_version = image_cache_version_counters.next_version + 1
RETURNING next_version;

-- name: PublishCacheVersion :exec
-- Records a CONFIRMED publication: version was minted by MintCacheVersion
-- above for the SAME cache_key, and the build that named it as its own
-- PublishVersion has reported success AND confirmed (ports.BuildOutcome.
-- PublishedCacheVersion) that its cache mount was not silently dropped.
-- Plain INSERT, never UPSERT/UPDATE -- (cache_key, version) is immutable
-- by construction: once a row exists here, it is NEVER updated or
-- reused, mirroring the "immutable snapshot" contract this whole design
-- exists to keep (a confirmed publish is a genuinely NEW fact, it never
-- overwrites an earlier version's own row any more than the underlying
-- storage overwrites its own bytes).
INSERT INTO image_cache_versions (cache_key, version, fingerprint, created_at)
VALUES ($1, $2, $3, now());

-- name: LatestCacheVersion :one
-- The most recently CONFIRMED-published version for cache_key -- what a
-- NEW build attempting this key should mount read-only as its own
-- MountVersion. pgx.ErrNoRows means no version has ever been confirmed
-- for this key yet (this key's very first build, or every prior attempt
-- failed before confirming) -- the caller's own degrade path
-- (MountVersion = "", an ordinary cold build with nothing to mount, still
-- requesting a PublishVersion for NEXT time) handles this exactly like
-- every other "nothing to build from yet" case elsewhere in this
-- codebase (e.g. GetImageBuild's own pgx.ErrNoRows contract).
SELECT version FROM image_cache_versions
WHERE cache_key = $1
ORDER BY version DESC
LIMIT 1;

-- name: ListCacheVersions :many
-- Every CONFIRMED version currently tracked for cache_key, newest first --
-- app/imagebuild.Builder's own retention-pruning input
-- (domain/imagebuild.PruneCacheVersions decides which of these rows to
-- drop; see that function's own doc comment and
-- domain/imagebuild.RetainedCacheVersions for the exact policy: keep the
-- newest N, prune the rest).
SELECT version FROM image_cache_versions
WHERE cache_key = $1
ORDER BY version DESC;

-- name: DeleteCacheVersions :exec
-- Removes this control plane's OWN bookkeeping rows for the given
-- (already decided by domain/imagebuild.PruneCacheVersions) versions.
-- Deliberately NOT a request to reclaim the underlying provider-side
-- bytes -- see ports.CacheMount's own doc comment and this migration's
-- own top doc comment for why that stays a documented, unimplemented
-- obligation on the build service, exactly mirroring image_builds' own
-- long-named, still-open image_ref GC gap. Safe to run against a version
-- an already-in-flight build resolved as its own MountVersion before this
-- ran: that build's wire request was already sent, naming an object the
-- provider may still hold -- only FUTURE LatestCacheVersion resolutions
-- stop offering a pruned row.
DELETE FROM image_cache_versions
WHERE cache_key = $1 AND version = ANY(sqlc.arg('versions')::bigint[]);
