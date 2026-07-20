DROP INDEX IF EXISTS events_session_id_message_id_idx;
ALTER TABLE events DROP COLUMN IF EXISTS message_id;
