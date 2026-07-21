-- Step 26 ("image builds", §8.5-note/§10-P2): one row per DISTINCT
-- fingerprint (Base + RepoSHAs + RuntimeVersion, hashed deterministically --
-- internal/domain/imagebuild.Fingerprint, sorted-map-keys so Go's own
-- randomized map iteration order never changes the result). The fingerprint
-- itself is a one-way SHA-256 hex digest, so the RAW inputs it was computed
-- from (base/repo_shas/runtime_version) are ALSO persisted here, not just
-- the hash -- app/imagebuild's own background builder (a SEPARATE process
-- tick, with no access to whichever session's dispatch.go call originally
-- computed the fingerprint) needs the real ports.ImageSpec fields back to
-- actually call SandboxProvider.BuildImage. This is deliberately MORE than
-- IMPLEMENTATION_PLAN.md's own "at minimum" column list names -- justified
-- because a hash cannot be un-hashed, and the background builder has no
-- other channel to learn what to build.
--
-- image_ref is nullable until a build first succeeds. status follows this
-- codebase's own established Postgres-enum convention (artifact_type,
-- sandbox_status, ...) rather than a TEXT + CHECK constraint.
-- attempt_count/last_attempt_at/next_retry_at back
-- domain/imagebuild.EvaluateBackoff's own exponential-backoff decision
-- (§3.5: "not fixed 30 min") and the failure-streak alert -- next_retry_at
-- NULL (a fresh pending row) or in the past means eligible to (re)attempt
-- now; see queries/image_builds.sql's own ListDueImageBuilds.
--
-- fingerprint is the primary key, not a separate surrogate id: a
-- (Base, RepoSHAs, RuntimeVersion) triple names EXACTLY one build target, so
-- "the row for this fingerprint" is the natural, only-ever-needed lookup key
-- (dispatch.go's own spawn-time GetImageBuild call, the background builder's
-- own claim query) -- no session/environment ever owns a row here, several
-- unrelated sessions with identical repo SHAs legitimately share one.
CREATE TYPE image_build_status AS ENUM ('pending', 'building', 'ready', 'failed');

CREATE TABLE image_builds (
    fingerprint     TEXT PRIMARY KEY,
    base            TEXT NOT NULL,
    repo_shas       JSONB NOT NULL DEFAULT '{}'::jsonb,
    runtime_version TEXT NOT NULL,
    image_ref       TEXT,
    status          image_build_status NOT NULL DEFAULT 'pending',
    attempt_count   INTEGER NOT NULL DEFAULT 0,
    last_attempt_at TIMESTAMPTZ,
    next_retry_at   TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Backs the background builder's own poll query (ListDueImageBuilds:
-- status = 'pending' OR (status = 'failed' AND next_retry_at <= now())) --
-- mirrors migrations/000020_session_timers_fires_at_idx.up.sql's own
-- identical "index the exact predicate the pump polls with" precedent.
CREATE INDEX idx_image_builds_status_next_retry_at ON image_builds (status, next_retry_at);
