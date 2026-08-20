-- Queries backing ReviewDigestSectionFeedbackStore (§26.5) -- see
-- migrations/000086_review_digest_section_feedback.up.sql's own doc
-- comment for the table's full design.

-- name: UpsertReviewDigestSectionFeedback :one
-- Idempotent create keyed on (comment_id, comment_type) -- mirrors
-- UpsertFalsePositivePattern's own identical "(xmax = 0) AS inserted"
-- self-referential-no-op-update idiom (reviewfalsepositivepatterns.sql)
-- exactly, for the identical reason: a contest is captured EXACTLY ONCE,
-- by its own triggering comment -- a redelivered webhook or a retried
-- command for the SAME (comment_id, comment_type) must leave every column
-- completely untouched, never re-inserted as a second row. Inserted (via
-- "xmax = 0") lets the caller log whether this call captured a genuinely
-- NEW contest or just re-observed an already-known (comment_id,
-- comment_type) pair, without a second SELECT.
INSERT INTO review_digest_section_feedback (repo_full_name, pr_number, section, content_hash, comment_type, comment_id, reason, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (comment_id, comment_type) DO UPDATE SET repo_full_name = review_digest_section_feedback.repo_full_name
RETURNING *, (xmax = 0) AS inserted;

-- name: ListReviewDigestSectionFeedback :many
-- §26.5's own per-repo audit view / contestation-rate KPI source: every
-- contest for one repo, optionally narrowed by section, newest first --
-- bounded by limit, mirroring ListFalsePositivePatterns' own identical
-- "bounded from day one" discipline (reviewfalsepositivepatterns.sql).
-- sqlc.narg('section') is NULL to mean "every section", matching this
-- codebase's own established optional-filter-argument convention.
SELECT * FROM review_digest_section_feedback
WHERE repo_full_name = $1 AND (sqlc.narg('section')::TEXT IS NULL OR section = sqlc.narg('section'))
ORDER BY created_at DESC
LIMIT $2;

-- name: CountReviewDigestSectionFeedback :one
-- The contestation-rate KPI's own numerator: how many contests exist for
-- one repo/section within a bounded window -- paired with a deep-path
-- verdict count (the denominator) at the call site, internal/app/
-- reviewverdict, mirroring ListReviewVerdictsInWindow's own identical
-- "bounded from day one" discipline (reviewverdicts.sql).
SELECT count(*) FROM review_digest_section_feedback
WHERE repo_full_name = $1 AND section = $2 AND created_at > $3;
