-- Queries backing FalsePositivePatternStore (Step 63, "review: learned
-- false-positive patterns", §22.2/§22.4) -- see
-- migrations/000073_review_false_positive_patterns.up.sql's own doc
-- comment for the table's full design.

-- name: UpsertFalsePositivePattern :one
-- Idempotent create keyed on (comment_id, comment_type) (§22.2: "keyed on
-- the triggering comment id" -- comment_type joins that key because
-- comment_id ALONE is not globally unique across GitHub's own
-- issue_comment vs pull_request_review_comment id sequences, see this
-- table's own migration doc comment) -- mirrors ClaimWebhookDelivery's
-- own "(xmax = 0) AS inserted" self-referential-no-op-update idiom
-- (webhookdeliveries.sql) exactly, NOT UpsertReviewFinding's "refresh one
-- bookkeeping column on conflict" idiom: unlike a finding, which is
-- legitimately re-reported on every later commit, a taught pattern is
-- captured EXACTLY ONCE, by its own triggering comment -- a redelivered
-- webhook or a retried command for the SAME (comment_id, comment_type)
-- must leave every column (most importantly retired_at/hit_count, this
-- row's own mutable lifecycle state) completely untouched, never reset
-- back to "just taught, active, zero hits". Inserted (via "xmax = 0")
-- lets the caller log whether this call captured a genuinely NEW pattern
-- or just re-observed an already-known (comment_id, comment_type) pair,
-- without a second SELECT.
INSERT INTO review_false_positive_patterns (repo_full_name, comment_id, comment_type, reason, created_by)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (comment_id, comment_type) DO UPDATE SET repo_full_name = review_false_positive_patterns.repo_full_name
RETURNING *, (xmax = 0) AS inserted;

-- name: GetFalsePositivePattern :one
-- Looked up by the retire endpoint (patternID comes straight off the URL
-- path, repoFullName off the route) -- SCOPED to repoFullName (audit fix:
-- previously keyed on id alone, letting a pattern belonging to a
-- DIFFERENT repo be retrieved through the wrong repo's URL) -- pgx.
-- ErrNoRows means no such pattern exists IN THIS REPO at all, distinct
-- from "exists (in this repo) but already retired"
-- (RetireFalsePositivePattern's own guarded UPDATE reports that case
-- separately -- see its own doc comment).
SELECT * FROM review_false_positive_patterns WHERE id = $1 AND repo_full_name = $2;

-- name: ListActiveFalsePositivePatterns :many
-- §22.3's own advisory-injection read: every currently-active (not
-- retired) pattern for one repo, oldest-first (a stable, deterministic
-- order, mirroring ListOpenAndRebuttedReviewFindings' own identical
-- "byte-for-byte-repeatable render" reasoning, reviewfindings.sql).
SELECT * FROM review_false_positive_patterns
WHERE repo_full_name = $1 AND retired_at IS NULL
ORDER BY created_at ASC;

-- name: ListFalsePositivePatterns :many
-- §22.4's own audit view: EVERY pattern for one repo, active or retired,
-- newest-first (the natural read order for an audit/review screen --
-- unlike ListActiveFalsePositivePatterns above, whose oldest-first order
-- exists only to make repeated prompt renders deterministic). Bounded by
-- limit, mirroring ListReviewFindingStatusesInWindow's own identical
-- "bounded from day one" discipline (reviewfindings.sql) -- there is no
-- unbounded "list everything, ever" query in this codebase for a
-- maintainer-authored, potentially long-lived table like this one.
SELECT * FROM review_false_positive_patterns
WHERE repo_full_name = $1
ORDER BY created_at DESC
LIMIT $2;

-- name: RetireFalsePositivePattern :one
-- §22.4's own retirement write: a maintainer+ permanently excludes a
-- stale/wrong pattern from future advisory injection. SCOPED to
-- repo_full_name (audit fix: previously keyed on id alone -- the
-- handler's own doc comment promises a 404 "if no pattern with this id
-- exists in this repo at all", but that was unreachable: a pattern
-- belonging to a DIFFERENT repo was happily retired through the wrong
-- repo's URL, a real API-contract violation and a silent wrong-repo
-- mutation via a stale UI id) AND guarded (WHERE retired_at IS NULL,
-- CLAUDE.md/§11's own "guarded UPDATE ... WHERE for cross-writer
-- transitions" rule) so retiring an ALREADY-retired pattern is a no-op
-- that returns pgx.ErrNoRows (never a second, silently overwriting
-- retired_at/retired_by) -- the caller (httpapi) tells "never existed in
-- this repo" apart from "exists in this repo but already retired" via a
-- follow-up GetFalsePositivePattern (also repo-scoped) read on this same
-- ErrNoRows path.
UPDATE review_false_positive_patterns
SET retired_at = now(), retired_by = $2
WHERE id = $1 AND repo_full_name = $3 AND retired_at IS NULL
RETURNING *;

-- name: IncrementFalsePositivePatternHitCount :exec
-- §22.4's own hit-count bookkeeping: bumped once per pattern for every
-- review pass that actually included it in the advisory block (§22.3:
-- "injected into every review pass, first pass and re-review alike") --
-- called with every active pattern's own id ListActiveFalsePositivePatterns
-- just returned, immediately after rendering that same list into the
-- advisory block (internal/app/reviewcontext.FetchFalsePositivePatterns).
-- Best-effort bookkeeping only: a failure here must never fail the
-- review turn whose prompt already carries the (successfully rendered)
-- advisory block -- see that function's own doc comment.
UPDATE review_false_positive_patterns
SET hit_count = hit_count + 1, last_hit_at = now()
WHERE id = ANY(sqlc.arg('ids')::uuid[]);
