DROP INDEX IF EXISTS sessions_parent_session_id_idx;
ALTER TABLE sessions DROP COLUMN spawn_depth;
ALTER TABLE sessions DROP COLUMN parent_session_id;
