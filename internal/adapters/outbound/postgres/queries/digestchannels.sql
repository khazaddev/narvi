-- Queries backing internal/app/digest's own channel-discovery step
-- (§21.3) -- "per-repo/per-channel ... reusing existing
-- session-thread association tables, never a second, separate repo<->
-- channel mechanism". Each query joins the SAME github_pr_sessions table
-- (repo_full_name) already used throughout this codebase's own review
-- machinery against an EXISTING channel-identity table
-- (slack_thread_sessions/linear_agent_sessions) -- no new table.

-- name: ListSlackChannelsForRepoSince :many
-- Every DISTINCT Slack channel a review session for repoFullName has
-- threaded through since sinceTime -- "this channel has recently hosted
-- this repo's own review activity", the one signal a digest's own
-- per-repo/per-channel scoping needs.
SELECT DISTINCT sts.channel_id
FROM slack_thread_sessions sts
JOIN github_pr_sessions gps ON gps.session_id = sts.session_id
WHERE gps.repo_full_name = $1 AND sts.created_at > $2;

-- name: ListLinearOrganizationsForRepoSince :many
-- The Linear sibling of ListSlackChannelsForRepoSince above --
-- organization_id is the closest existing granularity Linear's own
-- schema carries (linear_agent_sessions has no distinct "channel"/"team"
-- concept, migrations/000030's own doc comment); mirrors
-- linearNotifier.resolveAccessToken's own identical per-organization
-- credential scoping (internal/app/outboxworker/linearnotifier.go).
SELECT DISTINCT las.organization_id
FROM linear_agent_sessions las
JOIN github_pr_sessions gps ON gps.session_id = las.session_id
WHERE gps.repo_full_name = $1 AND las.created_at > $2;

-- name: ListDistinctReposWithRecentSessions :many
-- internal/app/digest.Pump's own per-tick repo enumeration -- every
-- DISTINCT repo_full_name github_pr_sessions has claimed since sinceTime,
-- bounded by limit (§21.1's own "bounded from day one" discipline). The
-- SAME "no canonical registry of every repo Narvi manages" gap
-- githubapi/listopenprs.go's own doc comment already names -- this is
-- the best available substitute, exactly like internal/app/automerge's
-- own reliance on repo_settings for its OWN repo enumeration.
SELECT DISTINCT repo_full_name FROM github_pr_sessions
WHERE claimed_at > $1
LIMIT $2;
