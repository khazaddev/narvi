-- Queries backing ReleaseManifestCheckStore (§12.2 item 9, §15.2/§15.3) -- see
-- migrations/000097_release_manifest_checks.up.sql's own doc comment for
-- the table's full design.

-- name: InsertReleaseManifestCheck :one
-- The one write internal/app/releasereview.Run makes once it has computed
-- this release PR's own manifest findings + aggregate-review trigger
-- decision -- alongside, never instead of, its existing outbox-delivered
-- comment (RenderManifestComment). Best-effort: a failure here is logged
-- and Run simply continues to its own existing outbox enqueue, mirroring
-- that function's own established "every internal failure is logged and
-- this function simply returns" posture.
INSERT INTO release_manifest_checks (
    session_id, repo_full_name, pr_number, base_ref, head_ref,
    constituent_pr_count, coverage_partial,
    aggregate_review_triggered, aggregate_review_trigger_reasons,
    findings, merged_prs
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: GetLatestReleaseManifestCheck :one
-- (repo_full_name, pr_number)'s own most-recently-computed check --
-- mirrors GetLatestReviewVerdict's own identical "one indexed lookup,
-- ORDER BY created_at DESC LIMIT 1" shape. pgx.ErrNoRows means no
-- manifest check has ever been persisted for this PR (a row predating
-- or a check whose own Run() insert failed and was only ever delivered as
-- a comment).
SELECT * FROM release_manifest_checks
WHERE repo_full_name = $1 AND pr_number = $2
ORDER BY created_at DESC
LIMIT 1;
