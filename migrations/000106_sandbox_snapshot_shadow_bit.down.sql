ALTER TABLE sandbox_history DROP COLUMN IF EXISTS snapshot_suppressed_in_shadow;
ALTER TABLE sandboxes DROP COLUMN IF EXISTS snapshot_suppressed_in_shadow;
