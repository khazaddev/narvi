-- Queries backing AutoApprovalOutcomeStore (Step 62, §21.2 stage 2) -- see
-- migrations/000070_auto_approval_outcomes.up.sql's own doc comment for
-- the contradiction-rate calibration read model's full design.

-- name: RecordAutoApprovalOutcome :exec
-- Idempotent per (repo_full_name, pr_number, head_sha) -- the SAME
-- "INSERT ... ON CONFLICT DO NOTHING" atomic-claim idiom github_pr_sessions/
-- repo_settings already establish. A second call recording a DIFFERENT
-- outcome for the same (repo, PR, head_sha) -- e.g. 'overridden' observed
-- after 'confirmed' was already recorded, which should not happen in
-- practice since a merged PR's own review_verdicts row cannot later
-- regain a fresh HasChangesRequested against that SAME now-closed PR --
-- is deliberately a no-op, never a silent overwrite: the FIRST recorded
-- outcome for a given verdict wins, mirroring review_findings' own
-- "first_seen_at is set once, never overwritten" precedent
-- (migrations/000046) for the analogous "durable first observation"
-- shape.
INSERT INTO auto_approval_outcomes (repo_full_name, pr_number, head_sha, outcome)
VALUES ($1, $2, $3, $4)
ON CONFLICT (repo_full_name, pr_number, head_sha) DO NOTHING;

-- name: CountAutoApprovalOutcomesInWindow :one
-- The contradiction-rate rollup's own single bounded aggregate query --
-- total (every recorded outcome) and contested (outcome = 'overridden')
-- counts for repoFullName since sinceTime, in one round trip.
-- internal/domain/reviewverdict.ContradictionRate reduces these two plain
-- integers -- this query does no rate arithmetic itself (§11: no
-- floating-point policy math in a SQL query the domain layer should
-- instead own and unit-test).
SELECT
    count(*) AS total,
    count(*) FILTER (WHERE outcome = 'overridden') AS contested
FROM auto_approval_outcomes
WHERE repo_full_name = $1 AND decided_at > $2;
