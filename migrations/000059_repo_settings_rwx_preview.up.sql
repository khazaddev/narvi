-- RWX preview links (Step 57, §4.1.2 point 1): three further nullable
-- columns on repo_settings (migrations/000044_repo_settings.up.sql's own
-- doc comment: "ADDED as further columns on this SAME table rather than
-- one bespoke table per toggle" -- Step 48's sentinel_autofix_enabled,
-- migrations/000048_repo_settings_sentinel_autofix.up.sql, already
-- extended it once this same way).
--
-- Unlike block_on_high_risk/sentinel_autofix_enabled (strict admin
-- booleans), enabling RWX previews needs three concrete values, not just
-- an on/off flag: dispatchKey selects which RWX run definition to
-- dispatch, endpointTemplate + orgSlug render the deterministic "friendly"
-- preview URL (§4.1.2 point 4) at enqueue time, with no build to await.
-- repo_settings remains the one shared home for "admin-configured,
-- per-repo policy" regardless of a given setting's own shape.
--
-- All three are required TOGETHER for previews to be considered enabled on
-- a repo -- application code (internal/app/sessionactor's own preview
-- enqueue path, pushpr.go) treats a partial configuration (e.g. a dispatch
-- key set but an empty endpoint template) identically to "absent", i.e.
-- OFF, never a half-working feature -- mirroring this table's own
-- established fail-closed-on-missing-row precedent (§24.5: "if the
-- setting cannot be read... treated as OFF"). Nullable, default NULL:
-- absent = off by default (§4.1.2 point 1: "absent = feature off").
ALTER TABLE repo_settings ADD COLUMN rwx_preview_dispatch_key TEXT;
ALTER TABLE repo_settings ADD COLUMN rwx_preview_endpoint_template TEXT;
ALTER TABLE repo_settings ADD COLUMN rwx_preview_org_slug TEXT;
