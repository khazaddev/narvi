ALTER TABLE sandbox_history DROP COLUMN IF EXISTS snapshot_id;

ALTER TABLE sandboxes DROP COLUMN IF EXISTS pending_snapshot_message_id;
ALTER TABLE sandboxes DROP COLUMN IF EXISTS snapshot_id;
