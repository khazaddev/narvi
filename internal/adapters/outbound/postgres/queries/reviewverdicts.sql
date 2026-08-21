-- Queries backing ReviewVerdictStore (§21.1) -- see
-- migrations/000067_review_verdicts.up.sql's own doc comment for the
-- table's full append-only design.

-- name: InsertReviewVerdict :one
-- The ONE write this table ever accepts -- always an INSERT, never an
-- UPDATE (see the table's own doc comment for why). Called from
-- httpapi.PostReviewVerdict (reviewverdict.go), inside the SAME
-- transaction as that handler's existing review_findings upserts and
-- outbox write. digest_summary/digest_arch_decisions/digest_stack_risks/
-- digest_unverified_limits (§26.1, migrations/
-- 000077_review_verdicts_digest.up.sql) and digest_description_adequacy/
-- digest_adequacy_explanation/digest_proposed_body (§26.2,
-- migrations/000078_review_verdicts_description_adequacy.up.sql) forward
-- internal/domain/reviewpost.Digest verbatim -- see those migrations' own
-- doc comments for why all seven stay nullable at the schema level
-- despite digest_summary/digest_description_adequacy/
-- digest_adequacy_explanation being APPLICATION-required on every new
-- post.
--
-- review_path (§26.3, migrations/
-- 000081_review_verdicts_review_path.up.sql) forwards turns.review_depth
-- verbatim -- nullable, NULL for a verdict posted before this Step
-- existed, or whose own turn never had a resolvable depth (the SAME
-- "safe, not dangerous, degradation" posture head_sha's own resolution
-- already has, reviewverdict.go).
--
-- counter_review/fact_check/fact_check_killed/digest_contested_points
-- (§26.4/§26.6, migrations/
-- 000084_review_verdicts_counter_review.up.sql) forward internal/domain/
-- review.CounterReviewStatus, internal/domain/reviewpost.FactCheckStatus/
-- FactCheckKilled/Digest.ContestedPoints verbatim -- see that migration's
-- own doc comment for why all four stay nullable at the schema level
-- despite fact_check being APPLICATION-required, unconditionally, on
-- every new post (unlike counter_review, deep-path-only-required).
INSERT INTO review_verdicts (
    repo_full_name, pr_number, head_sha,
    risk_level, premise, blast_radius, files_changed, tests_coverage, docs_drift,
    proposed_shippable, shippable, session_id,
    digest_summary, digest_arch_decisions, digest_stack_risks, digest_unverified_limits,
    digest_description_adequacy, digest_adequacy_explanation, digest_proposed_body,
    review_path,
    counter_review, fact_check, fact_check_killed, digest_contested_points
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24)
RETURNING *;

-- name: GetLatestReviewVerdict :one
-- The DISTINCT ON (repo, pr_number) ... ORDER BY created_at DESC
-- reduction §21.1 specifies -- scoped here to ONE (repo_full_name,
-- pr_number) pair (the one shape every real caller -- the auto-approval
-- eligibility engine, the decision inbox's own classification, the
-- revalidate-at-click/at-merge paths -- actually needs), so this is a
-- plain indexed lookup ORDER BY created_at DESC LIMIT 1, not a
-- multi-row DISTINCT ON scan -- see ListLatestAutoApprovedInRepo below
-- for the multi-PR, per-repo shape that DOES need real DISTINCT ON.
-- pgx.ErrNoRows means no verdict has ever been posted for this PR.
SELECT * FROM review_verdicts
WHERE repo_full_name = $1 AND pr_number = $2
ORDER BY created_at DESC
LIMIT 1;

-- name: ListLatestAutoApprovedInRepo :many
-- internal/app/automerge's own discovery query (§21.2 stage 2): every
-- open-as-of-last-review-candidate PR in repoFullName whose LATEST
-- verdict, within the bounded window, is Shippable == 'auto' -- the real
-- multi-row DISTINCT ON (repo, pr_number) ... ORDER BY created_at DESC
-- reduction §21.1 names, THEN filtered to shippable = 'auto' in an outer
-- query (DISTINCT ON's own "first row per group" pick must be decided by
-- created_at alone, before shippable can be tested, so the filter cannot
-- fold into the same SELECT's own WHERE clause). This is a DISCOVERY
-- aid only, bounded and cheap (no GitHub call) -- internal/app/
-- decisioninbox.RevalidateForAutoMerge is what actually re-confirms each
-- candidate live before anything merges (§21.2: "reuses the decision
-- inbox's existing server-side re-validation-at-click contract
-- unchanged"), so a candidate this query returns that has since gone
-- stale (a new commit landed, CI flipped) is simply rejected there, never
-- trusted as authority here.
SELECT * FROM (
    SELECT DISTINCT ON (repo_full_name, pr_number) *
    FROM review_verdicts
    WHERE repo_full_name = $1 AND created_at > $2
    ORDER BY repo_full_name, pr_number, created_at DESC
) latest
WHERE shippable = 'auto'
ORDER BY created_at ASC
LIMIT $3;

-- name: ListReviewVerdictsForPR :many
-- §26.1 item 5's own merge-readout "History" rail (§12.2 item 2): every
-- verdict ever posted for ONE (repo_full_name, pr_number), newest first,
-- bounded by limit -- the SAME "bounded from day one" discipline §21.1
-- requires of every query against this table (ListReviewVerdictsInWindow
-- below is the repo-wide analytics sibling; this is the PR-scoped one no
-- existing caller needed before now).
SELECT * FROM review_verdicts
WHERE repo_full_name = $1 AND pr_number = $2
ORDER BY created_at DESC
LIMIT $3;

-- name: ListReviewVerdictsInWindow :many
-- The analytics rollups' own shared bounded scan (§21.1: "every query
-- against this history is bounded from day one") -- every verdict for
-- repoFullName posted after sinceTime, oldest first. internal/app/
-- reviewverdict's own Timeseries/TopRiskDrivers functions both reduce
-- this SAME result set in memory (a pure, already-fetched-data
-- transform, mirroring internal/domain/decisioninbox.MedianLatency's own
-- "caller fetches, pure package reduces" split) rather than each issuing
-- its own bespoke aggregate SQL query.
SELECT * FROM review_verdicts
WHERE repo_full_name = $1 AND created_at > $2
ORDER BY created_at ASC
LIMIT $3;
