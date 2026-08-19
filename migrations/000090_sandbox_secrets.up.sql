-- sandbox_secrets: this codebase's SECOND generic secret-storage table
-- (Step 72, "sandbox secrets & opencode config", §27.1) -- a deliberately
-- SEPARATE table from provider_credentials (migrations/
-- 000056_provider_credentials.up.sql), not a widened ENUM on that one.
--
-- provider_credentials' own identity column (provider) is a closed,
-- code-controlled ENUM of 3 known LLM provider names, each mapped to
-- fixed OpenCode env-var name(s) by domain code
-- (providercredential.EnvVarNames), consumed by exactly one process
-- (opencode serve). sandbox_secrets inverts both properties: its identity
-- (name) is a user-CHOSEN env-var name, validated fail-closed at the
-- application layer (internal/domain/sandboxsecret.ValidateName) rather
-- than constrained by a Postgres ENUM (an ENUM cannot express "any POSIX-
-- shaped name except these specific reserved ones"), and its consumers
-- are the WHOLE supervised process tree a session spawns (hooks,
-- services.yml services, opencode serve itself) -- see
-- internal/domain/sandboxsecret's own doc.go for the full "why a second
-- table" reasoning.
--
-- sandbox_secret_scope is a REAL Postgres ENUM, matching provider_
-- credential_scope's own precedent for a small, closed, code-controlled
-- vocabulary -- but its OWN 4 values, not provider_credential_scope's:
-- 'automation' here (never on provider_credentials -- §25.3 scopes that
-- table to repo/environment/global/user only) and no 'user' here (never
-- applicable to a sandbox-wide secret -- there is no per-user identity
-- concept for a hook/service env var the way there is for a personally-
-- linked ChatGPT OAuth credential).
--
-- 'automation' is SCHEMA-ONLY as of this Step (§27.1's own explicit
-- carve-out, mirroring migration 000045's already-established "reserved
-- in the design vocabulary, not actually built" precedent for a different
-- column entirely): nothing in this codebase yet writes an
-- automation-scoped row (no CRUD endpoint exists for it), and the
-- sandbox-facing delivery endpoint (internal/adapters/inbound/httpapi/
-- sandboxsecretsdelivery.go) never resolves a candidate at this scope
-- either -- both are the deferred per-automation-secrets follow-up's own
-- scope (§8.4/Step 52's own explicit deferral, internal/domain/
-- automation/doc.go). The value ships NOW purely so that follow-up needs
-- no second migration, exactly as automation/doc.go already promised.
--
-- Resolution order (automation -> environment -> repo -> global, most
-- specific wins) runs through internal/domain/providercredential's OWN
-- Scope/Resolve -- see that package's own doc.go for why sandbox_secrets
-- reuses provider_credentials' resolution vocabulary rather than
-- reinventing it, and internal/domain/providercredential/scope.go's own
-- ScopeAutomation for the "fourth scopePriority row" §27.1 asks for.
CREATE TYPE sandbox_secret_scope AS ENUM ('automation', 'environment', 'repo', 'global');

-- scope_target_id is a single, polymorphic TEXT column whose meaning
-- depends on scope -- mirrors provider_credentials.scope_target_id's own
-- exact convention (migrations/000056_provider_credentials.up.sql), plus
-- the new automation case:
--   - scope = 'automation':  automations.id (migrations/
--     000051_automations.up.sql), stringified. Schema-only -- see above;
--     no row at this scope exists in practice until the deferred
--     per-automation-secrets follow-up ships.
--   - scope = 'repo':        the "owner/repo" natural key --
--     repo_settings.repo_full_name's own EXACT shape, same as
--     provider_credentials' own repo-scoped rows.
--   - scope = 'environment': environments.id (migrations/
--     000021_environments.up.sql), stringified.
--   - scope = 'global':      always NULL -- enforced by the CHECK
--     constraint below, identical to provider_credentials' own
--     scope=global convention.
--
-- value_encrypted is AES-256-GCM ciphertext produced by
-- platform.EncryptToken (internal/platform/tokenencrypt.go) -- the EXACT
-- same encryption-at-rest mechanism provider_credentials.value_encrypted
-- already uses, reused directly rather than a new crypto scheme invented
-- for this Step. NEVER logged, NEVER returned by the management REST API
-- (write-only from that API's own perspective, mirroring
-- providercredentials.go's own posture exactly) -- see
-- internal/adapters/inbound/httpapi/sandboxsecrets.go's own doc comment.
--
-- name has NO CHECK constraint of its own at this layer -- shape
-- validation (POSIX env-var shape, the NARVI_* reservation, the
-- providercredential.EnvVarNames collision rejection) lives entirely in
-- Go (internal/domain/sandboxsecret.ValidateName), enforced once at the
-- CRUD write path, mirroring how provider_credentials' own NUL-byte
-- rejection is a Go-layer check with no DB-layer mirror either. A regex
-- CHECK constraint here would duplicate that logic in a second language
-- with its own drift risk, for a table where every write already goes
-- through exactly one Go code path (unlike, say, an externally-populated
-- table where a DB-layer backstop would earn its keep).
CREATE TABLE sandbox_secrets (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    scope            sandbox_secret_scope NOT NULL,
    scope_target_id  TEXT,
    name             TEXT NOT NULL,
    value_encrypted  BYTEA NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT sandbox_secrets_scope_target_id_shape CHECK (
        (scope = 'global' AND scope_target_id IS NULL) OR
        (scope <> 'global' AND scope_target_id IS NOT NULL AND scope_target_id <> '')
    )
);

-- At most one secret per (scope, scope_target_id, name) for an
-- automation/repo/environment-scoped row -- a plain UNIQUE constraint
-- would never collide on NULL scope_target_id (Postgres's own documented
-- NULLs-are-distinct behavior), so scoped rows (scope_target_id NOT NULL)
-- and the single global row per name (scope_target_id IS NULL) each get
-- their OWN partial unique index -- the EXACT same pair-of-partial-
-- indexes idiom provider_credentials_scoped_uniq/
-- provider_credentials_global_uniq already establish (migration 000056),
-- reused verbatim per §27.1's own "Step 53's idioms reused throughout"
-- instruction.
CREATE UNIQUE INDEX sandbox_secrets_scoped_uniq
    ON sandbox_secrets (scope, scope_target_id, name)
    WHERE scope_target_id IS NOT NULL;
CREATE UNIQUE INDEX sandbox_secrets_global_uniq
    ON sandbox_secrets (name)
    WHERE scope_target_id IS NULL;

-- Lookup index for the delivery endpoint's own resolution query
-- (internal/adapters/inbound/httpapi/sandboxsecretsdelivery.go), which
-- fetches every candidate row across every scope for a session's own
-- repo(s)/environment in one query -- mirrors
-- provider_credentials_scope_target_id_idx's own identical reasoning and
-- shape.
CREATE INDEX sandbox_secrets_scope_target_id_idx
    ON sandbox_secrets (scope_target_id)
    WHERE scope_target_id IS NOT NULL;
