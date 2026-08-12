ALTER TABLE github_pr_sessions DROP COLUMN IF EXISTS auto_retrigger_budget_notice_sent_at;
ALTER TABLE github_pr_sessions DROP COLUMN IF EXISTS auto_retrigger_count;
ALTER TABLE github_pr_sessions DROP COLUMN IF EXISTS pending_retrigger_head_sha;
