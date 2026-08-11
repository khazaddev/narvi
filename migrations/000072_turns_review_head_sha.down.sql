ALTER TABLE github_pr_sessions ADD COLUMN pending_head_sha TEXT;
ALTER TABLE turns DROP COLUMN IF EXISTS review_head_sha;
