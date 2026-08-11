ALTER TABLE repo_settings DROP COLUMN IF EXISTS sensitive_blast_radius_tags;
ALTER TABLE repo_settings DROP COLUMN IF EXISTS max_auto_approve_files_changed;
ALTER TABLE repo_settings DROP COLUMN IF EXISTS auto_merge_enabled;
