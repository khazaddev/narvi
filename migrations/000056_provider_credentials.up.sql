-- provider_credentials: this codebase's FIRST generic secret-storage
-- table (Step 53, "provider credential injection", §25.1/§25.3). Grepped
-- directly before writing this, twice: no `CREATE TABLE ... secret`
-- anywhere else under migrations/, no `SecretStore` type anywhere under
-- internal/ -- authz.ActionManageRepoSecrets/ActionManageEnvSecrets/
-- ActionManageGlobalSecrets (internal/domain/authz/action.go) have been
-- reserved, with no table to act on, since that Step (see this codebase's
-- own established "reserved in the RBAC/design vocabulary, not actually
-- built" precedent, migration 000045's own comment on parent_session_id/
-- spawn_depth, for a different mechanism entirely).
--
-- Scope, deliberately narrow, per §25.3 verbatim: "provider API keys
-- only, mapped provider->env-var name ... sourced per-repo/
-- per-environment/global exactly like the RBAC matrix already
-- anticipates." No per-automation secrets here (§8.4/Step 52's own
-- explicit deferral into this Step is for the underlying INFRA only --
-- automations consuming it is a further, later, focused follow-up, per
-- internal/domain/automation/doc.go's own doc comment).
--
-- provider_credential_provider is a real Postgres ENUM, matching this
-- codebase's own automation_status precedent (migrations/
-- 000051_automations.up.sql) for a small, closed, code-controlled
-- vocabulary -- not a CHECK-constrained TEXT column. Same for
-- provider_credential_scope.
CREATE TYPE provider_credential_provider AS ENUM ('google', 'anthropic', 'openai');
CREATE TYPE provider_credential_scope AS ENUM ('repo', 'environment', 'global');

-- scope_target_id is a single, polymorphic TEXT column whose meaning
-- depends on scope:
--   - scope = 'repo':        the "owner/repo" natural key --
--     repo_settings.repo_full_name's own EXACT shape (migrations/
--     000044_repo_settings.up.sql), derived from a session's own repo
--     clone URL via internal/domain/reposource.ParseOwnerRepo. Chosen
--     over environments.id's own UUID shape specifically because a repo
--     has no standalone "repos" table/id anywhere in this codebase to
--     reference (sessions.repos is a bare JSONB array) -- repo_full_name
--     is this codebase's own only stable repo identity.
--   - scope = 'environment': environments.id (migrations/
--     000021_environments.up.sql), stringified -- an environments row
--     genuinely has a UUID primary key to reference, unlike a repo.
--   - scope = 'global':      always NULL (there is no target to scope to
--     at all) -- enforced by the CHECK constraint below, mirroring
--     workflow_bindings' own future "(lane, repo_full_name = NULL)"
--     global-row convention already described in TECHNICAL_PLAN.md §25.4
--     (not yet built -- Step 54 -- but the same "NULL means global"
--     convention this Step establishes first).
--
-- value_encrypted is AES-256-GCM ciphertext produced by
-- platform.EncryptToken (internal/platform/tokenencrypt.go) -- the EXACT
-- same encryption-at-rest mechanism identities.access_token_encrypted
-- already uses for GitHub OAuth tokens (migrations/000017_auth_v1.up.sql),
-- reused directly rather than a new crypto scheme invented for this Step.
-- NEVER logged, NEVER returned by the management REST API (write-only
-- from that API's own perspective) -- see internal/adapters/inbound/
-- httpapi/providercredentials.go's own doc comment.
CREATE TABLE provider_credentials (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    scope            provider_credential_scope NOT NULL,
    scope_target_id  TEXT,
    provider         provider_credential_provider NOT NULL,
    value_encrypted  BYTEA NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT provider_credentials_scope_target_id_shape CHECK (
        (scope = 'global' AND scope_target_id IS NULL) OR
        (scope <> 'global' AND scope_target_id IS NOT NULL AND scope_target_id <> '')
    )
);

-- At most one credential per (scope, scope_target_id, provider) for a
-- repo/environment-scoped row -- a plain UNIQUE constraint would never
-- collide on NULL scope_target_id (Postgres's own documented NULLs-are-
-- distinct behavior), so repo/environment rows (scope_target_id NOT NULL)
-- and the single global row per provider (scope_target_id IS NULL) each
-- get their OWN partial unique index -- mirrors
-- automations_webhook_token_hash_uniq's own identical "WHERE <col> IS NOT
-- NULL" partial-unique-index precedent (migrations/
-- 000055_automations_triggers_and_extras.up.sql) for a nullable column
-- that must still be unique wherever it IS set.
CREATE UNIQUE INDEX provider_credentials_scoped_uniq
    ON provider_credentials (scope, scope_target_id, provider)
    WHERE scope_target_id IS NOT NULL;
CREATE UNIQUE INDEX provider_credentials_global_uniq
    ON provider_credentials (provider)
    WHERE scope_target_id IS NULL;

-- Lookup index for the delivery endpoint's own resolution query
-- (internal/adapters/inbound/httpapi/providercredentialsdelivery.go),
-- which fetches every candidate row across all 3 scopes for a session's
-- own repo(s)/environment in one query -- scope_target_id is the only
-- column that query filters on beyond the always-true "scope = 'global'"
-- branch, so it is the one worth a dedicated index (the two UNIQUE
-- indexes above already cover every other lookup shape this table needs).
CREATE INDEX provider_credentials_scope_target_id_idx
    ON provider_credentials (scope_target_id)
    WHERE scope_target_id IS NOT NULL;
