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

-- UpsertAutoApprovalSettings (Step 62, §21.2) is REMOVED as of §62
-- review finding C5 (MEDIUM but a privilege boundary, fixed) -- it wrote
-- all three auto-approval/auto-merge columns together, even though the
-- TWO REST endpoints that ever called it (PutAutoApprovalSettings,
-- gated by ActionConfigureAutoApprove; PutAutoMergeToggle, admin-only
-- ActionToggleAutoMerge -- httpapi/reposettings.go) are separately
-- gated and each owns a DIFFERENT subset of these columns. Both
-- handlers worked around this via an app-layer read-modify-write (read
-- the OTHER endpoint's own columns first, pass them straight through
-- unchanged) -- but that read-modify-write is exactly the kind of race
-- this fix closes: a maintainer's PutAutoApprovalSettings write and an
-- admin's PutAutoMergeToggle write, landing concurrently, could each
-- read the OTHER's stale pre-write value and silently clobber it on
-- write-back, including reverting a toggle an admin just armed/
-- disarmed. Replaced by the two column-scoped upserts below --
-- UpsertAutoMergeToggle/UpsertAutoApprovalEligibility -- each touching
-- ONLY the columns its own one caller owns, so two concurrent writers
-- touching DIFFERENT columns can no longer race at the DATABASE level,
-- closing the hazard by construction rather than by an app-layer
-- read-then-preserve convention a future edit could easily forget.

-- name: UpsertAutoMergeToggle :one
-- §62 review finding C5's own fix: idempotent create-or-update of ONLY
-- auto_merge_enabled (migrations/000069_repo_settings_auto_approval.up.sql)
-- -- mirrors UpsertRWXPreviewSettings' own identical "touches ONLY these
-- columns, ON CONFLICT leaves every other column untouched" shape.
-- max_auto_approve_files_changed/sensitive_blast_radius_tags are left
-- COMPLETELY untouched by this query -- their own column DEFAULT (NULL)
-- when this creates a brand-new row, or whatever a PRIOR
-- UpsertAutoApprovalEligibility call already set, on an update.
INSERT INTO repo_settings (repo_full_name, auto_merge_enabled, updated_at)
VALUES ($1, $2, now())
ON CONFLICT (repo_full_name)
DO UPDATE SET auto_merge_enabled = EXCLUDED.auto_merge_enabled, updated_at = now()
RETURNING *;

-- name: UpsertAutoApprovalEligibility :one
-- §62 review finding C5's own fix: idempotent create-or-update of ONLY
-- max_auto_approve_files_changed/sensitive_blast_radius_tags -- the
-- column-scoped sibling of UpsertAutoMergeToggle immediately above (see
-- that query's own doc comment for the full "why"). auto_merge_enabled
-- is left COMPLETELY untouched by this query -- its own column DEFAULT
-- (false) when this creates a brand-new row, or whatever a PRIOR
-- UpsertAutoMergeToggle call already set, on an update.
INSERT INTO repo_settings (repo_full_name, max_auto_approve_files_changed, sensitive_blast_radius_tags, updated_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (repo_full_name)
DO UPDATE SET max_auto_approve_files_changed = EXCLUDED.max_auto_approve_files_changed, sensitive_blast_radius_tags = EXCLUDED.sensitive_blast_radius_tags, updated_at = now()
RETURNING *;

-- name: UpsertAutoRetriggerReviewToggle :one
-- Step 65's own admin-only, per-repo opt-in (§24.5, migrations/
-- 000076_repo_settings_auto_retrigger_review.up.sql) -- idempotent
-- create-or-update of ONLY auto_retrigger_review_enabled, mirroring
-- UpsertAutoMergeToggle's own identical column-scoped shape immediately
-- above (§62 review finding C5's fix, generalized to this further,
-- independently-gated toggle): every other repo_settings column is left
-- COMPLETELY untouched, so a concurrent write to any of them (an admin's
-- PutRepoSettings, PutAutoMergeToggle, or PutAutoApprovalSettings call)
-- can never race with this one at the database level.
INSERT INTO repo_settings (repo_full_name, auto_retrigger_review_enabled, updated_at)
VALUES ($1, $2, now())
ON CONFLICT (repo_full_name)
DO UPDATE SET auto_retrigger_review_enabled = EXCLUDED.auto_retrigger_review_enabled, updated_at = now()
RETURNING *;

-- name: ListAutoMergeEnabledRepos :many
-- internal/app/automerge's own per-tick repo enumeration (§21.2 stage
-- 2): every repo an admin has armed -- mirrors this table's own
-- established "repo_settings is the one shared home for admin-configured
-- per-repo policy" precedent; there is no separate registry of "every
-- repo Narvi manages" anywhere in this codebase (internal/adapters/
-- outbound/githubapi/listopenprs.go's own doc comment names this same
-- gap), so a repo with auto_merge_enabled = true is, by construction,
-- already a repo this deployment has touched.
SELECT * FROM repo_settings WHERE auto_merge_enabled = true;

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
