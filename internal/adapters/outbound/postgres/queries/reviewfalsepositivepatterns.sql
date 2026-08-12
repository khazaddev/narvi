-- Queries backing FalsePositivePatternStore (Step 63, "review: learned
-- false-positive patterns", §22.2/§22.4) -- see
-- migrations/000073_review_false_positive_patterns.up.sql's own doc
-- comment for the table's full design.

-- name: UpsertFalsePositivePattern :one
-- Idempotent create keyed on comment_id (§22.2: "keyed on the triggering
-- comment id") -- mirrors ClaimWebhookDelivery's own "(xmax = 0) AS
-- inserted" self-referential-no-op-update idiom (webhookdeliveries.sql)
-- exactly, NOT UpsertReviewFinding's "refresh one bookkeeping column on
-- conflict" idiom: unlike a finding, which is legitimately re-reported
-- on every later commit, a taught pattern is captured EXACTLY ONCE, by
-- its own triggering comment -- a redelivered webhook or a retried
-- command for the SAME comment_id must leave every column (most
-- importantly retired_at/hit_count, this row's own mutable lifecycle
-- state) completely untouched, never reset back to "just taught,
-- active, zero hits". Inserted (via "xmax = 0") lets the caller log
-- whether this call captured a genuinely NEW pattern or just re-observed
-- an already-known comment_id, without a second SELECT.
INSERT INTO review_false_positive_patterns (repo_full_name, comment_id, reason, created_by)
VALUES ($1, $2, $3, $4)
ON CONFLICT (comment_id) DO UPDATE SET repo_full_name = review_false_positive_patterns.repo_full_name
RETURNING *, (xmax = 0) AS inserted;

-- name: GetFalsePositivePattern :one
-- Looked up by the retire endpoint (patternID comes straight off the URL
-- path) -- pgx.ErrNoRows means no such pattern exists at all, distinct
-- from "exists but already retired" (RetireFalsePositivePattern's own
-- guarded UPDATE reports that case separately -- see its own doc
-- comment).
SELECT * FROM review_false_positive_patterns WHERE id = $1;

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
-- stale/wrong pattern from future advisory injection. Guarded (WHERE
-- retired_at IS NULL, CLAUDE.md/§11's own "guarded UPDATE ... WHERE for
-- cross-writer transitions" rule) so retiring an ALREADY-retired pattern
-- is a no-op that returns pgx.ErrNoRows (never a second, silently
-- overwriting retired_at/retired_by) -- the caller (httpapi) tells "never
-- existed" apart from "already retired" via a follow-up
-- GetFalsePositivePattern read on this same ErrNoRows path.
UPDATE review_false_positive_patterns
SET retired_at = now(), retired_by = $2
WHERE id = $1 AND retired_at IS NULL
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
