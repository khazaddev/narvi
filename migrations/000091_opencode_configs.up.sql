-- opencode_configs: Step 72's own ("sandbox secrets & opencode config",
-- §27.2) storage for org-authored OpenCode engine config (opencode.json)
-- -- global and per-Environment documents an admin/maintainer edits in
-- Settings, injected into OpenCode's own documented config slots at boot
-- (internal/adapters/inbound/httpapi/opencodeconfigdelivery.go,
-- cmd/sandbox-agent/main.go).
--
-- document is PLAINTEXT JSONB, deliberately -- unlike sandbox_secrets
-- (migrations/000090_sandbox_secrets.up.sql) and provider_credentials
-- (migrations/000056_provider_credentials.up.sql), this table stores NO
-- encrypted material at all. This is configuration a human reads and
-- edits in Settings, not secret material: anything secret-shaped
-- (API keys, tokens) belongs in sandbox_secrets and is referenced FROM a
-- document here via OpenCode's own `{env:VAR}` substitution, which
-- resolves at OpenCode's own load time from the process environment
-- sandbox-agent already built (§27.2) -- this table never holds a value
-- that substitution would resolve, only the `{env:VAR}` reference itself.
--
-- opencode_config_scope has only 2 values -- 'environment'/'global' --
-- deliberately NO 'repo' and NO 'automation': §27.2 is explicit that
-- OpenCode config injection targets exactly OpenCode's own global slot
-- (~/.config/opencode/opencode.json) and its custom slot (the
-- OPENCODE_CONFIG env var, one per Environment) -- a repo's own
-- COMMITTED opencode.json already occupies OpenCode's own "project" slot
-- natively (no Narvi table needed to represent it at all), and there is
-- no per-automation OpenCode config concept anywhere in §27's design.
--
-- scope_target_id mirrors provider_credentials.scope_target_id/
-- sandbox_secrets.scope_target_id's own polymorphic-TEXT convention:
--   - scope = 'environment': environments.id (migrations/
--     000021_environments.up.sql), stringified.
--   - scope = 'global':      always NULL -- enforced by the CHECK
--     constraint below, identical to the other 2 tables' own scope=global
--     convention.
--
-- "At most one global row, one per environment" (§27.2) -- unlike
-- provider_credentials/sandbox_secrets, there is no third dimension
-- (provider/name) fanning a single scope target out into multiple rows:
-- one Environment has AT MOST one opencode_configs row, full stop, and
-- there is AT MOST one global row in the whole deployment. The partial-
-- unique-index PAIR below is the same shape as the other 2 tables'
-- idiom, simplified to that 1-row-per-scope-target reality: the
-- environment-scoped index is unique on (scope, scope_target_id) alone
-- (no third column to also partition by), and the global-scoped index is
-- unique on (scope) alone WHERE scope_target_id IS NULL -- since the
-- CHECK constraint guarantees every scope_target_id-IS-NULL row has
-- scope='global', that index can only ever admit ONE such row, which is
-- exactly "at most one global row" enforced structurally rather than by
-- convention.
CREATE TYPE opencode_config_scope AS ENUM ('environment', 'global');

CREATE TABLE opencode_configs (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    scope            opencode_config_scope NOT NULL,
    scope_target_id  TEXT,
    document         JSONB NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT opencode_configs_scope_target_id_shape CHECK (
        (scope = 'global' AND scope_target_id IS NULL) OR
        (scope = 'environment' AND scope_target_id IS NOT NULL AND scope_target_id <> '')
    )
);

CREATE UNIQUE INDEX opencode_configs_scoped_uniq
    ON opencode_configs (scope, scope_target_id)
    WHERE scope_target_id IS NOT NULL;
CREATE UNIQUE INDEX opencode_configs_global_uniq
    ON opencode_configs (scope)
    WHERE scope_target_id IS NULL;
