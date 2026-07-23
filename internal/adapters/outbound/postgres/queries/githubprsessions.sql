-- Queries backing GitHubPRSessionStore (§8.2's "atomic claim coalescing of
-- concurrent @mentions", Step 32 "GitHub ingress"). See
-- migrations/000028_github_pr_sessions.up.sql's own doc comment for the
-- full two-step claim design (ensure the row exists via ON CONFLICT, then
-- lock + branch via FOR UPDATE) these three queries implement together.

-- name: EnsureGitHubPRSessionRow :exec
-- Idempotently ensures a (repo_full_name, pr_number) claim row exists,
-- with session_id left NULL on a fresh insert -- the SAME
-- "INSERT ... ON CONFLICT" atomic-claim idiom ClaimWebhookDelivery (Step
-- 31) already establishes. DO NOTHING here since this step's only job is
-- "make sure the row is there to lock next" -- the following
-- LockGitHubPRSessionForUpdate call, in the SAME transaction, is what
-- actually determines who won.
INSERT INTO github_pr_sessions (repo_full_name, pr_number)
VALUES ($1, $2)
ON CONFLICT (repo_full_name, pr_number) DO NOTHING;

-- name: LockGitHubPRSessionForUpdate :one
-- Locks the (repo_full_name, pr_number) row for the remainder of the
-- caller's own transaction -- any concurrent caller's own call to this
-- SAME query for the SAME PR blocks here until the first caller commits
-- or rolls back, mirroring internal/adapters/inbound/httpapi/turn.go's
-- own GetActorEpochForUpdate row-lock precedent exactly. The caller must
-- have already run EnsureGitHubPRSessionRow (in the SAME transaction) so
-- this row is guaranteed to exist by the time this runs.
SELECT session_id FROM github_pr_sessions
WHERE repo_full_name = $1 AND pr_number = $2
FOR UPDATE;

-- name: SetGitHubPRSessionID :exec
-- Fills in session_id for a (repo_full_name, pr_number) claim row while
-- still holding LockGitHubPRSessionForUpdate's own row lock -- called
-- exactly once, by whichever caller observed session_id NULL under that
-- lock (the genuine first mention on this PR), immediately after it
-- creates the real session, still inside the SAME transaction as both the
-- lock and the session insert.
UPDATE github_pr_sessions
SET session_id = $3
WHERE repo_full_name = $1 AND pr_number = $2;
