-- Queries backing RepoSettingsStore (§8.2/Step 47, §21.2): a small,
-- extensible table of admin-configured, per-repo policy flags -- see
-- migrations/000044_repo_settings.up.sql's own doc comment for the full
-- "one shared table, not one bespoke table per toggle" design rationale.

-- name: GetRepoSettings :one
-- A missing row (pgx.ErrNoRows) means every flag defaults to its own safe
-- value -- callers must NOT treat that as an error, see this table's own
-- migration doc comment.
SELECT * FROM repo_settings WHERE repo_full_name = $1;

-- name: UpsertRepoSettings :one
-- Idempotent create-or-update, keyed on repo_full_name -- an admin
-- (re)setting block_on_high_risk always writes the full, current desired
-- value rather than patching a delta, so a concurrent double-submit from
-- the same admin settles on whichever write lands last, never a
-- non-deterministic partial merge.
INSERT INTO repo_settings (repo_full_name, block_on_high_risk, updated_at)
VALUES ($1, $2, now())
ON CONFLICT (repo_full_name)
DO UPDATE SET block_on_high_risk = EXCLUDED.block_on_high_risk, updated_at = now()
RETURNING *;
