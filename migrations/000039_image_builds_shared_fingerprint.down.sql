ALTER TABLE image_builds DROP COLUMN IF EXISTS built_at;
ALTER TABLE image_builds DROP COLUMN IF EXISTS built_repo_shas;
ALTER TABLE image_builds RENAME COLUMN repo_urls TO repo_shas;
