-- review_digest_section_feedback: Step 69's own §26.5 measurement table
-- ("measuring the readout"): "per-section digest feedback extending the
-- finding-outcome read model (§21.1) -- contest/confirm per digest
-- section, plus a maintainer command `arch recap wrong: <reason>`
-- mirroring Step 63's `false positive:` command exactly". Mirrors
-- review_false_positive_patterns' own table shape precisely (migrations/
-- 000073_review_false_positive_patterns.up.sql, that table's own doc
-- comment) -- see internal/domain/archrecap's own doc comment for why
-- this is a STRUCTURALLY DIFFERENT table despite the identical capture-
-- command shape: a contest is a per-PR, per-digest-section measurement
-- signal, never repo-scoped standing advisory content injected back into
-- future reviews the way a taught false-positive pattern is.
--
-- comment_id/comment_type mirror review_false_positive_patterns' own
-- identical idempotency key and its own doc comment's full "why" (GitHub's
-- issue_comment and pull_request_review_comment id sequences are NOT
-- globally unique against each other) -- the UNIQUE constraint is on the
-- PAIR, never comment_id alone, for the identical reason.
--
-- section is internal/domain/reviewpost.DigestSection's own fixed
-- vocabulary (summary/arch_recap/stack_risks/unverified_limits) -- v1
-- ships exactly ONE capture command, targeting 'arch_recap' only
-- (§26.5's own scope), but this column is not itself narrowed to that one
-- value: the read model (contest/confirm counts per section) is written
-- to generalize from day one, per IMPLEMENTATION_PLAN.md's Step 69 row
-- ("per-section digest feedback ... contest/confirm per digest section"),
-- even though only one section has a dedicated capture command today.
--
-- content_hash is internal/domain/reviewpost.ComputeDigestSectionIdentity's
-- own sha256 hex digest of the CONTESTED digest section's own persisted
-- text (reviewpost.ArchRecapText(digest.archDecisions) for section =
-- 'arch_recap', the latest review_verdicts row on record for this PR at
-- the moment the capture command was processed) -- §22.1's identity
-- discipline extended to digest sections: "never by section index or
-- position, which would suffer the exact churn-fragility problem §22.1
-- already solved for findings". repo_full_name/pr_number are still stored
-- (and indexed) separately, never derived from content_hash alone, since
-- every real read this table serves (the per-repo contestation-rate KPI,
-- the per-PR audit view) is scoped by repo/PR, not by an opaque hash.
--
-- reason is the maintainer's own free-text explanation, taken verbatim
-- from the text after the `arch recap wrong:` prefix
-- (internal/domain/archrecap.Match) -- untrusted, PR-thread-authored
-- content; this table is a measurement/audit signal only, never re-
-- injected into any review's own prompt (unlike review_false_positive_
-- patterns' own advisory-injection use).
CREATE TABLE review_digest_section_feedback (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_full_name TEXT NOT NULL,
    pr_number      INTEGER NOT NULL,
    section        TEXT NOT NULL,
    content_hash   TEXT NOT NULL,
    -- comment_type is the GitHub webhook event type that carried the
    -- triggering comment -- "issue_comment" or "pull_request_review_comment"
    -- (see this table's own top doc comment for why, paired with
    -- comment_id, this is the real idempotency key, not comment_id alone).
    comment_type   TEXT NOT NULL,
    comment_id     BIGINT NOT NULL,
    reason         TEXT NOT NULL,
    -- created_by is nullable (ON DELETE SET NULL, mirroring review_
    -- false_positive_patterns.created_by's own identical precedent) --
    -- always populated in practice (the capture command's own dispatch-
    -- before-router gate, domain/authz.Authorize(ActionContestArchRecap),
    -- requires a LINKED actor to reach this INSERT at all).
    created_by     UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (comment_id, comment_type)
);

-- The per-repo, per-section contestation-rate KPI's own read query
-- (§26.5: "digest precision (contestation rate)") -- every contest for one
-- repo, optionally narrowed by section, newest first (the natural read
-- order for an audit/KPI screen, mirroring review_false_positive_
-- patterns' own ListFalsePositivePatterns precedent, reviewfalsepositivepatterns.sql).
CREATE INDEX review_digest_section_feedback_repo_section_idx
    ON review_digest_section_feedback (repo_full_name, section, created_at);

-- The per-(repo, pr, section, content_hash) reconciliation lookup a
-- future contest against the SAME still-current digest section content
-- needs -- e.g. surfacing "already contested" in a UI, or de-duplicating
-- the underlying signal across multiple maintainers independently
-- contesting the identical recap.
CREATE INDEX review_digest_section_feedback_content_hash_idx
    ON review_digest_section_feedback (repo_full_name, pr_number, section, content_hash);
