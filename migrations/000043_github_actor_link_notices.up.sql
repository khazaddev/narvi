-- github_actor_link_notices: anti-spam dedupe for the "please sign in via
-- GitHub OAuth" reply now posted (batch fix/deny-unlinked-github-actors)
-- whenever an UNLINKED GitHub commenter's mention is denied by
-- internal/adapters/inbound/github/coalesce.go's CreateOrJoin (either
-- gate: WINNER-path create-session, or REUSE-path prompt-existing-
-- session, both now calling actorauthz.AuthorizeLinkedActor
-- unconditionally instead of short-circuiting to allowed=true/skipping
-- the check entirely for an unresolved actor).
--
-- Scoped per (repo_full_name, pr_number, commenter_id) -- one row per
-- still-unlinked commenter, per PR they have mentioned the bot on --
-- deliberately narrower than a global per-identity dedupe (which
-- identity_link_prompts, migrations/000036, already provides for a
-- DIFFERENT purpose: Slack/Linear's own magic-link auto-linking). See the
-- design decision this migration implements (docs/TECHNICAL_PLAN.md §13.2
-- and this batch's own PR description) for the full comparison against
-- webhook_deliveries/github_pr_sessions/identity_link_prompts and why none
-- of those three existing tables fit this exact dedupe key.
--
-- No nonce/magic-link column: unlike identity_link_prompts, this table
-- backs no clickable, completable link at all -- GitHub has none (see
-- internal/app/actorauthz/authorize.go's own AuthorizeLinkedActor doc
-- comment) -- it purely records "have we already told this commenter,
-- on this PR, to go sign in" so a repeat mention within the TTL window
-- doesn't re-post the identical reply on every single comment.
--
-- notified_at is looked up and compared against
-- platform.Timeouts.GitHubActorNoticeTTL by the caller (mirrors
-- identity_link_prompts' own GetLatestForProviderAndExternalID + TTL-
-- compare pattern, internal/app/identitylink/service.go's own
-- createOrReuseLinkPrompt) -- this table itself makes no TTL judgment.
CREATE TABLE github_actor_link_notices (
    repo_full_name TEXT NOT NULL,
    pr_number      INTEGER NOT NULL,
    commenter_id   BIGINT NOT NULL,
    notified_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (repo_full_name, pr_number, commenter_id)
);
