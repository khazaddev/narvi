-- review_false_positive_patterns: Step 63's own repo-scoped table of
-- maintainer-taught false-positive descriptions (§22.2). A maintainer+
-- teaches a free-text pattern ONCE ("that's not actually a problem in
-- this repo") so the same class of non-issue does not need re-litigating
-- on every subsequent review -- see internal/domain/falsepositive's own
-- doc comment for the capture command and internal/domain/reviewpost.
-- RenderAlreadyAnsweredFacts' own sibling, internal/domain/falsepositive.
-- RenderAdvisoryBlock, for how these rows are injected back into a review
-- (§22.3, advisory only, NEVER a filter -- nothing in this table's own
-- shape or any query over it is ever consulted to silently drop a
-- finding).
--
-- Deliberately its OWN table, sitting beside review_findings exactly as
-- that table's own migration (000046_review_findings.up.sql) anticipated
-- ("Step 59's own learned-pattern table [Step 63, post-renumbering] ...
-- can sit beside it") -- a DIFFERENT thing entirely: review_findings
-- tracks ONE finding's own lifecycle on ONE pull request; this table
-- tracks a maintainer's standing, repo-wide TEACHING that applies to
-- EVERY future review of that repo, independent of any single PR.
--
-- comment_id is "the triggering comment id" (§22.2) -- the GitHub
-- issue_comment/pull_request_review_comment id whose body carried the
-- capturing `false positive: <reason>` command
-- (internal/domain/falsepositive.Match). comment_id ALONE is NOT globally
-- unique: this feature captures BOTH GitHub `issue_comment` and
-- `pull_request_review_comment` events into this SAME column, and GitHub
-- allocates those two event types' own ids from two SEPARATE, currently-
-- overlapping numeric sequences (verified live against the real GitHub
-- API) -- so a plain UNIQUE (comment_id) risks a cross-event-type
-- collision silently no-oping the upsert (ON CONFLICT returning the
-- OTHER row) and corrupting the audit log with the wrong repo/reason
-- attributed to the wrong pattern id. comment_type records which of the
-- two event types actually carried this comment (the exact eventType
-- string, "issue_comment" or "pull_request_review_comment" --
-- internal/adapters/inbound/github's own eventTypeIssueComment/
-- eventTypePullRequestReviewComment constants, payload.go), and the
-- UNIQUE constraint is on the PAIR (comment_id, comment_type), never
-- comment_id alone -- exactly like webhook_deliveries' (provider,
-- delivery_id) and github_pr_sessions' (repo_full_name, pr_number)
-- before it (migrations/000027, migrations/000028), this pair is the
-- real, collision-free idempotency key: a redelivered webhook or a
-- retried command for the SAME (comment_id, comment_type) must never
-- double-insert a second pattern row. Deliberately NOT
-- (repo_full_name, comment_id) either -- that would still allow a
-- same-repo issue-vs-review-comment collision, since the two event
-- types' id sequences overlap WITHIN a single repo too, not merely
-- across repos. repo_full_name is still stored (and indexed) separately,
-- never derived from comment_id, because every real read this table
-- serves (the advisory-injection fetch, the per-repo audit view) is
-- scoped by repo, not by comment.
--
-- Lifecycle columns (§22.4, shipped in this SAME migration, never a
-- deferred follow-up):
--   - hit_count/last_hit_at: incremented every time this (still-active)
--     pattern is actually INJECTED into a review pass's advisory block
--     (internal/app/reviewcontext.FetchFalsePositivePatterns) -- a usage
--     signal for the audit view ("has anyone even seen this pattern
--     recently"), not a claim that it ever caused an agent to dismiss
--     anything (§22.3 forbids this table from ever knowing that: the
--     injection is advisory prose the agent weighs, not a rule with an
--     observable "fired" event).
--   - retired_at/retired_by: NULL means active (eligible for injection);
--     non-NULL means a maintainer+ has explicitly retired this pattern
--     (a wrong or stale pattern), permanently excluded from future
--     injection but kept, not deleted, for the audit trail.
CREATE TABLE review_false_positive_patterns (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_full_name TEXT NOT NULL,
    comment_id     BIGINT NOT NULL,
    -- comment_type is the GitHub webhook event type that carried the
    -- triggering comment -- "issue_comment" or "pull_request_review_comment"
    -- (see this table's own top doc comment for why this, paired with
    -- comment_id, is the real idempotency key, not comment_id alone).
    comment_type   TEXT NOT NULL,
    -- reason is the maintainer's own free-text pattern description, taken
    -- verbatim from the text after the `false positive:` prefix
    -- (internal/domain/falsepositive.Match) -- untrusted, PR-thread-
    -- authored content, rendered back into a future review's prompt only
    -- inside an explicitly-delimited, explicitly-untrusted advisory block
    -- (§5.2, §22.3), never as an instruction.
    reason         TEXT NOT NULL,
    -- created_by is nullable (ON DELETE SET NULL, mirroring review_findings.
    -- rebutted_by's own identical precedent) -- always populated in
    -- practice (the capture command's own dispatch-before-router gate,
    -- domain/authz.Authorize(ActionTeachFalsePositivePattern), requires a
    -- LINKED actor to reach this INSERT at all; see
    -- internal/adapters/inbound/github's own capture handler), nullable
    -- purely so a later deletion of that user account never blocks
    -- deleting the user row.
    created_by     UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    hit_count      INTEGER NOT NULL DEFAULT 0,
    last_hit_at    TIMESTAMPTZ,
    retired_at     TIMESTAMPTZ,
    retired_by     UUID REFERENCES users(id) ON DELETE SET NULL,

    UNIQUE (comment_id, comment_type)
);

-- The one read query the advisory-injection path needs (§22.3): every
-- ACTIVE (not retired) pattern for one repo, in a stable, deterministic
-- order -- mirrors review_findings_repo_pr_status_idx's own identical
-- "index the exact WHERE clause the one real read query uses" precedent
-- (migrations/000046). Partial (WHERE retired_at IS NULL) since the
-- advisory-injection read NEVER wants a retired row, and the audit
-- view's own separate "every pattern, including retired" read is a
-- low-volume, maintainer-only, per-repo query with no comparable
-- per-review-turn frequency to optimize for.
CREATE INDEX review_false_positive_patterns_repo_active_idx
    ON review_false_positive_patterns (repo_full_name, created_at)
    WHERE retired_at IS NULL;
