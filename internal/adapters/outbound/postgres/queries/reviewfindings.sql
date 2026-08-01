-- Queries backing ReviewFindingStore (Step 48, "sentinels + suggestions",
-- §17/§22.1) -- see migrations/000046_review_findings.up.sql's own doc
-- comment for the full table design.

-- name: UpsertReviewFinding :one
-- The one write every verdict post with findings makes, once per finding
-- (internal/adapters/inbound/httpapi/reviewverdict.go) -- idempotent
-- create-or-update keyed on (repo_full_name, pr_number, identity_hash),
-- mirroring repo_settings'/github_pr_sessions' own established "INSERT
-- ... ON CONFLICT" idiom. Critically, ONLY last_seen_head_sha/last_seen_at
-- are overwritten on a re-report of the SAME identity -- status,
-- rebuttal_text, rebutted_by, rebutted_at, fix_child_session_id, and
-- fix_pr_number are all PRESERVED (never reset back to 'open'/NULL),
-- which is exactly what makes a finding re-reported on a LATER commit
-- still read as "already rebutted"/"already fix_open" rather than
-- silently reverting to looking brand new.
INSERT INTO review_findings (
    repo_full_name, pr_number, identity_hash, sentinel_kind, severity,
    file_path, line, description, suggested_fix
)
VALUES ($1, $2, $3, sqlc.narg('sentinel_kind'), $4, $5, sqlc.narg('line'), $6, sqlc.narg('suggested_fix'))
ON CONFLICT (repo_full_name, pr_number, identity_hash)
DO UPDATE SET last_seen_at = now()
RETURNING *;

-- name: GetReviewFinding :one
-- Looked up by the rebut/apply-suggestion endpoints (identityHash comes
-- straight off the URL path).
SELECT * FROM review_findings
WHERE repo_full_name = $1 AND pr_number = $2 AND identity_hash = $3;

-- name: ListOpenAndRebuttedReviewFindings :many
-- §22.1's own reconciliation read: every 'open' or 'rebutted' finding for
-- one PR, oldest-first (a stable, deterministic order — see
-- internal/domain/reviewpost.RenderAlreadyAnsweredFacts' own doc comment
-- on why ordering matters for a byte-for-byte-repeatable render). 'open'
-- is included deliberately, not just 'rebutted': an already-reported-but-
-- not-yet-rebutted finding should still read as "already told you about
-- this" to a re-reviewing agent, not as brand new -- see reviewpost.
-- ReconciledFinding's own doc comment. fix_pending/fix_open/fix_merged/
-- fix_applied findings are DELIBERATELY excluded here: those already have
-- their own separate, stronger signal (a referenced fix PR, or a merged
-- one) that the verdict-update step (§17.3) posts directly onto the PR,
-- and re-surfacing them in the SAME "already answered, do not re-report"
-- block a plain rebuttal uses would blur two different kinds of
-- resolution together.
SELECT * FROM review_findings
WHERE repo_full_name = $1 AND pr_number = $2 AND status IN ('open', 'rebutted')
ORDER BY first_seen_at ASC;

-- name: MarkReviewFindingRebutted :one
-- The rebuttal endpoint's own write (POST .../findings/{identityHash}/rebut,
-- maintainer+ only, authz.ActionEditReviewVerdict) -- sets status,
-- rebuttal_text, rebutted_by, rebutted_at together, atomically.
UPDATE review_findings
SET status = 'rebutted', rebuttal_text = $4, rebutted_by = $5, rebutted_at = now()
WHERE repo_full_name = $1 AND pr_number = $2 AND identity_hash = $3
RETURNING *;

-- name: MarkReviewFindingFixPending :one
-- Set the moment the sentinel-auto-fix outbox worker claims this finding
-- for a child session (§17.2) -- suppresses the manual apply-suggestion
-- action from this point on (§17.3: "the two remediation paths are
-- mutually exclusive per finding").
UPDATE review_findings
SET status = 'fix_pending', fix_child_session_id = $4
WHERE repo_full_name = $1 AND pr_number = $2 AND identity_hash = $3
RETURNING *;

-- name: MarkReviewFindingFixOpen :one
-- Set once the fix session's own fix PR has actually been opened
-- (pushpr.go's own createSentinelFixPRBestEffort).
UPDATE review_findings
SET status = 'fix_open', fix_pr_number = $2
WHERE fix_child_session_id = $1 AND status = 'fix_pending'
RETURNING *;

-- name: MarkReviewFindingFixApplied :one
-- Set by the manual apply-suggestion endpoint once it has successfully
-- committed a finding's own SuggestedFix directly (§12.2 item 2) -- the
-- OTHER of the two mutually-exclusive remediation paths.
UPDATE review_findings
SET status = 'fix_applied'
WHERE repo_full_name = $1 AND pr_number = $2 AND identity_hash = $3
RETURNING *;

-- name: MarkReviewFindingsFixMergedByFixSession :many
-- Merge-gating's own terminal write (§17.4, once all four checks pass and
-- the fix PR actually merges) -- every finding this ONE fix session was
-- addressing transitions to 'fix_merged' together.
UPDATE review_findings
SET status = 'fix_merged'
WHERE fix_child_session_id = $1 AND status IN ('fix_pending', 'fix_open')
RETURNING *;
