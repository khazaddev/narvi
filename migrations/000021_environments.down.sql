ALTER TABLE sessions DROP COLUMN IF EXISTS provenance_tag;
ALTER TABLE sessions DROP COLUMN IF EXISTS environment_id;
DROP TABLE IF EXISTS environments;
