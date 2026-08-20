-- Queries backing HandoffSentinelStore ("handoff-readiness
-- sentinel", §14.4) -- see migrations/000049_handoff_sentinel_runs.up.sql's
-- own doc comment for the full table design and its single-step claim
-- idiom.

-- name: ClaimHandoffSentinelRun :one
-- Atomic first-writer-wins claim on (repo_full_name, pr_number): ON
-- CONFLICT DO NOTHING means a caller that loses the race gets ZERO rows
-- back (a :one query with no matching row surfaces pgx.ErrNoRows,
-- unwrapped, to the caller -- HandoffSentinelStore.Claim's own doc
-- comment) -- simpler than sentinel_fixes'/github_pr_sessions' own
-- two-step InsertIfAbsent+GetForUpdate idiom because this caller never
-- needs the ALREADY-CLAIMED row's own data back, only a yes/no answer
-- (§17.1's own claim needs the existing row's fix_child_session_id;
-- this one does not have an equivalent follow-on read).
INSERT INTO handoff_sentinel_runs (repo_full_name, pr_number, session_id)
VALUES ($1, $2, $3)
ON CONFLICT (repo_full_name, pr_number) DO NOTHING
RETURNING id;
