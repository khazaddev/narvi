DROP INDEX IF EXISTS idx_repo_settings_demotion_sweep_pending;
ALTER TABLE repo_settings DROP COLUMN demotion_sweep_pending_at;
