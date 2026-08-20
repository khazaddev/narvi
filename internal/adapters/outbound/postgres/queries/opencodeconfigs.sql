-- Queries backing OpenCodeConfigStore ("sandbox secrets &
-- opencode config", §27.2) -- migrations/000091_opencode_configs.up.sql.
-- document is plaintext JSONB end to end -- unlike sandboxsecrets.sql/
-- providercredentials.sql, nothing here ever touches
-- platform.EncryptToken/DecryptToken at all (this table's own top
-- migration comment: "PLAINTEXT JSONB, deliberately").
--
-- Two separate Upsert queries, not one generic one, because
-- 'environment' and 'global' scope rows are governed by two DIFFERENT
-- partial unique indexes (opencode_configs_scoped_uniq vs
-- opencode_configs_global_uniq, migration 000091's own top comment) --
-- Postgres's ON CONFLICT clause names exactly one arbiter index per
-- statement, and the two indexes cover different column sets ((scope,
-- scope_target_id) vs (scope) WHERE scope_target_id IS NULL), so a single
-- parameterized statement cannot target both. Each query below hardcodes
-- its own scope literal for the same reason
-- UpsertOAuthProviderCredential (queries/providercredentials.sql)
-- hardcodes scope='user' -- a scope-specific write path, not a generic
-- one.

-- name: UpsertEnvironmentOpenCodeConfig :one
-- Create-or-replace for the (at most one) opencode_configs row belonging
-- to one Environment -- backs PUT /api/environments/{environmentID}/
-- opencode-config.
INSERT INTO opencode_configs (scope, scope_target_id, document)
VALUES ('environment', $1, $2)
ON CONFLICT (scope, scope_target_id) WHERE scope_target_id IS NOT NULL
DO UPDATE SET document = EXCLUDED.document, updated_at = now()
RETURNING *;

-- name: UpsertGlobalOpenCodeConfig :one
-- Create-or-replace for the (at most one, deployment-wide) global
-- opencode_configs row -- backs PUT /api/opencode-config.
INSERT INTO opencode_configs (scope, scope_target_id, document)
VALUES ('global', NULL, $1)
ON CONFLICT (scope) WHERE scope_target_id IS NULL
DO UPDATE SET document = EXCLUDED.document, updated_at = now()
RETURNING *;

-- name: GetEnvironmentOpenCodeConfig :one
-- pgx.ErrNoRows means "no config document set for this Environment yet"
-- -- an ordinary, expected state (§27.2's own "at most one... per
-- environment", never "exactly one"), not an error condition; the
-- httpapi GET handler renders this as 404.
SELECT * FROM opencode_configs WHERE scope = 'environment' AND scope_target_id = $1;

-- name: GetGlobalOpenCodeConfig :one
-- Same not-yet-configured convention as GetEnvironmentOpenCodeConfig
-- above.
SELECT * FROM opencode_configs WHERE scope = 'global' AND scope_target_id IS NULL;

-- name: DeleteEnvironmentOpenCodeConfig :execrows
DELETE FROM opencode_configs WHERE scope = 'environment' AND scope_target_id = $1;

-- name: DeleteGlobalOpenCodeConfig :execrows
DELETE FROM opencode_configs WHERE scope = 'global' AND scope_target_id IS NULL;

-- name: ListOpenCodeConfigsForDelivery :many
-- The delivery endpoint's own single, session-scoped read (§27.2:
-- "delivered at boot... both scopes at once"): the global row (always
-- included, if it exists) and this session's own environment-scoped row
-- (if any) -- returns 0, 1, or 2 rows; the caller
-- (opencodeconfigdelivery.go) splits the result by its own Scope field,
-- never a Resolve-style winner-take-all pick -- unlike sandbox_secrets/
-- provider_credentials, BOTH documents are delivered and composed by
-- OpenCode's own merge order, never narrowed to one winner here.
-- environment_id is sqlc.narg, NULL when the session has no attached
-- Environment (matches nothing -- scope_target_id is NEVER NULL for an
-- environment-scoped row, mirroring ListSandboxSecretsForResolution's own
-- identical convention).
SELECT * FROM opencode_configs
WHERE scope = 'global'
   OR (scope = 'environment' AND scope_target_id IS NOT NULL AND scope_target_id = sqlc.narg('environment_id'));
