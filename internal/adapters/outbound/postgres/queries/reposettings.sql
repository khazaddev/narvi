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
-- (re)setting block_on_high_risk/sentinel_autofix_enabled always writes
-- the full, current desired value rather than patching a delta, so a
-- concurrent double-submit from the same admin settles on whichever write
-- lands last, never a non-deterministic partial merge. sentinel_autofix_
-- enabled (Step 48, §17.1) is this SAME table's own further admin-only,
-- per-repo boolean, exactly as migrations/000044's own doc comment
-- anticipated -- see migrations/000048_repo_settings_sentinel_autofix.up.sql.
INSERT INTO repo_settings (repo_full_name, block_on_high_risk, sentinel_autofix_enabled, updated_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (repo_full_name)
DO UPDATE SET block_on_high_risk = EXCLUDED.block_on_high_risk, sentinel_autofix_enabled = EXCLUDED.sentinel_autofix_enabled, updated_at = now()
RETURNING *;

-- name: UpsertRWXPreviewSettings :one
-- Step 57 ("RWX provider + previews", §4.1.2 point 1): idempotent
-- create-or-update of ONLY the three RWX-preview columns (migrations/
-- 000059_repo_settings_rwx_preview.up.sql), keyed on repo_full_name --
-- deliberately independent of UpsertRepoSettings above: block_on_high_risk/
-- sentinel_autofix_enabled are the review domain's own admin-only
-- booleans; RWX previews are a separately-toggled setting with a
-- different shape entirely. This upsert touches ONLY these three columns
-- -- ON CONFLICT leaves whatever the other two already hold (their own
-- DEFAULT false when this creates a brand-new row) completely untouched.
INSERT INTO repo_settings (repo_full_name, rwx_preview_dispatch_key, rwx_preview_endpoint_template, rwx_preview_org_slug, updated_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (repo_full_name)
DO UPDATE SET rwx_preview_dispatch_key = EXCLUDED.rwx_preview_dispatch_key, rwx_preview_endpoint_template = EXCLUDED.rwx_preview_endpoint_template, rwx_preview_org_slug = EXCLUDED.rwx_preview_org_slug, updated_at = now()
RETURNING *;
