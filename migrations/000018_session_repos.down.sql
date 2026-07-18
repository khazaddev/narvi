ALTER TABLE sandboxes DROP COLUMN IF EXISTS last_spawn_failure_at;
ALTER TABLE sandboxes DROP COLUMN IF EXISTS spawn_failure_count;
ALTER TABLE sandboxes DROP COLUMN IF EXISTS provider_id;
ALTER TABLE turns DROP COLUMN IF EXISTS plan_mode;
ALTER TABLE turns DROP COLUMN IF EXISTS model_id;
ALTER TABLE turns DROP COLUMN IF EXISTS prompt;
ALTER TABLE sessions DROP COLUMN IF EXISTS opencode_conversation_id;
ALTER TABLE sessions DROP COLUMN IF EXISTS repos;
