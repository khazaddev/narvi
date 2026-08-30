DROP INDEX IF EXISTS auto_approval_outcomes_repo_decided_idx;
CREATE INDEX auto_approval_outcomes_repo_decided_idx ON auto_approval_outcomes (repo_full_name, decided_at DESC);
ALTER TABLE auto_approval_outcomes DROP COLUMN suppressed_in_shadow;
