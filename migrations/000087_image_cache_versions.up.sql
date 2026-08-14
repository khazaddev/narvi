-- Step 43(c), third iteration: immutable versioned cache snapshots.
--
-- The first two attempts at a build-time dependency cache both let a
-- shared, persistent cache volume be observed mid-mutation by a concurrent
-- reader -- attempt 1 mounted it read-write on the false premise that every
-- well-known cache path is content-addressed; attempt 2 narrowed the
-- window to a single post-success write-back, but that write-back was
-- ITSELF an unguarded writer into the same volume other builds could be
-- reading from at the same moment. This design removes the write window
-- entirely rather than narrowing it further: nothing is ever mutated in
-- place. Every successful build publishes a brand-new, immutable version;
-- a build mounts exactly one specific, already-published version,
-- read-only, and can never observe a version published after it started
-- (there is nothing to observe -- publishing creates an object, it never
-- touches one that already exists). See ports.CacheMount's own doc
-- comment (internal/app/ports/createspec.go) for the full contract this
-- schema backs.
--
-- Two tables, not one, because "reserve a version number" and "confirm
-- that version was actually published" are genuinely separate events with
-- different failure modes:
--
--   - image_cache_version_counters holds exactly one row per cache key
--     (domain/imagebuild.CacheVolumeKey(base, runtimeVersion) -- no
--     rotation epoch anymore, see that function's own doc comment for why
--     epoch became redundant once versions are immutable) and hands out
--     the next version number for that key, atomically, via a plain
--     UPSERT-increment (a single-row UPDATE is already safe under
--     Postgres's own row-level locking -- concurrent claimants simply
--     serialize on that one row, exactly the kind of small, bounded
--     contention this codebase already accepts elsewhere for a per-key
--     counter). A number reserved here but never confirmed in
--     image_cache_versions below (the build failed before publishing, or
--     the adapter declined the mount and fell back to an ordinary cold
--     build) is simply never reused -- a harmless gap in this key's own
--     sequence, exactly like a Postgres SERIAL's own gap-on-rollback
--     behavior.
--
--   - image_cache_versions is the durable, APPEND-ONLY history of every
--     version this control plane has confirmed was actually published --
--     one row per (cache_key, version), inserted only after the build
--     that named that version as its own PublishVersion has reported
--     success back through BuildImage AND confirmed (via ports.
--     BuildOutcome.PublishedCacheVersion) that its cache mount was not
--     silently dropped by the adapter's own decline-and-retry-cold
--     fallback. A row here is NEVER updated -- once inserted, it names an
--     immutable fact ("this version was published, by this fingerprint,
--     at this time") for as long as the row exists at all. The most
--     recent row for a cache_key (ORDER BY version DESC) is the "latest
--     confirmed" version app/imagebuild.Builder resolves as a NEW build's
--     own MountVersion.
--
-- Retention (domain/imagebuild.RetainedCacheVersions,
-- domain/imagebuild.PruneCacheVersions): app/imagebuild.Builder deletes
-- rows from image_cache_versions beyond the newest RetainedCacheVersions
-- for a cache_key immediately after every successful publish. This is
-- deliberately ONLY a deletion of this control plane's OWN bookkeeping
-- row -- it is never a request to reclaim the underlying bytes at the
-- provider (see ports.CacheMount's own doc comment for why that remains a
-- documented, unimplemented obligation on the build service, exactly
-- mirroring image_builds' own long-named, still-open image_ref GC gap).
-- Pruning a row is therefore always safe against an already-in-flight
-- build that resolved the pruned version as its own MountVersion before
-- the prune ran: that build's wire request was already sent, naming an
-- object the provider may still hold: only FUTURE MountVersion
-- resolutions stop offering the pruned version. A build that DOES try to
-- mount a version the provider has separately, independently reclaimed
-- degrades exactly like any other cache-mount trouble (the adapter's own
-- decline-and-retry-cold fallback, now recognizing a "version not found"
-- structured code) -- never a failure, per the port's own pure-accelerator
-- rule.
CREATE TABLE image_cache_version_counters (
    cache_key TEXT PRIMARY KEY,
    next_version BIGINT NOT NULL DEFAULT 1
);

CREATE TABLE image_cache_versions (
    cache_key TEXT NOT NULL,
    version BIGINT NOT NULL,
    fingerprint TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (cache_key, version)
);

-- Backs both LatestCacheVersion's own "ORDER BY version DESC LIMIT 1" and
-- ListCacheVersions' own retention-pruning scan -- both always filter on
-- cache_key first, then want newest-first ordering.
CREATE INDEX idx_image_cache_versions_cache_key_version ON image_cache_versions (cache_key, version DESC);
