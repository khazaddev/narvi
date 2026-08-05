DROP INDEX IF EXISTS automations_cron_trigger_idx;
DROP INDEX IF EXISTS automations_webhook_token_hash_uniq;

ALTER TABLE automations DROP COLUMN IF EXISTS artifact_summary;
ALTER TABLE automations DROP COLUMN IF EXISTS last_run_status;
ALTER TABLE automations DROP COLUMN IF EXISTS last_run_at;

ALTER TABLE automations DROP COLUMN IF EXISTS env_vars;

ALTER TABLE automations DROP COLUMN IF EXISTS sandbox_contracts_path;
ALTER TABLE automations DROP COLUMN IF EXISTS sandbox_mock_configured;
ALTER TABLE automations DROP COLUMN IF EXISTS sandbox_path_scope;

ALTER TABLE automations DROP COLUMN IF EXISTS last_cron_fired_at;
ALTER TABLE automations DROP COLUMN IF EXISTS webhook_token_hash;
ALTER TABLE automations DROP COLUMN IF EXISTS trigger_config;
ALTER TABLE automations DROP COLUMN IF EXISTS trigger_type;

DROP TYPE IF EXISTS automation_trigger_type;
