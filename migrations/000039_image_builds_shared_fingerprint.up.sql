-- Step 41 ("warm boot: shared fingerprint + spawn-path simplification",
-- §19.1) redefines domain/imagebuild.Fingerprint outright: the map keyed
-- per repo name now carries that repo's normalized clone URL, never its
-- resolved SHA (see that package's own doc comment for the full
-- reasoning -- one shared image per distinct repo set, refreshed
-- continuously from each repo's default-branch tip, rather than one image
-- per exact SHA combination). A fingerprint computed under this new
-- scheme can never equal one computed under the old (base, repoSHAs,
-- runtimeVersion) scheme, so every row this table holds today is keyed on
-- a definition that no longer exists -- and image_builds is a pure cache,
-- never a system of record (migrations/000024_image_builds.up.sql's own
-- doc comment), so there is nothing here worth preserving across the
-- redefinition. Per §19.1's own explicit instruction ("existing rows are
-- simply dropped as part of the migration that adds the columns below"),
-- this migration TRUNCATEs the table rather than attempting any
-- data-preserving column-only ALTER.
--
-- repo_shas is renamed to repo_urls: the column's own JSONB shape
-- (map[repo name]value) is unchanged, only what the value MEANS changes
-- -- matching the new UpsertPendingImageBuild query/Go param naming
-- (queries/image_builds.sql), so the column name stays honest about what
-- it actually holds.
--
-- built_repo_shas/built_at are new, nullable columns, populated ONLY once
-- a build actually succeeds (RecordImageBuildSuccess): the CONCRETE
-- per-repo SHAs (and the timestamp) that specific successful build
-- actually used. This is exactly the shape §19.2's own later freshness
-- pump needs (comparing a 'ready' row's built_repo_shas against each
-- repo's CURRENT default-branch tip to decide whether a refresh is due) --
-- landed here, one Step early, since the column is part of this Step's
-- own row shape and adding it later would be a second migration for no
-- reason. NULL for every row until its first successful build; a
-- 'pending'/'building'/'failed' row has never had one.
TRUNCATE image_builds;

ALTER TABLE image_builds RENAME COLUMN repo_shas TO repo_urls;
ALTER TABLE image_builds ADD COLUMN built_repo_shas JSONB;
ALTER TABLE image_builds ADD COLUMN built_at TIMESTAMPTZ;
