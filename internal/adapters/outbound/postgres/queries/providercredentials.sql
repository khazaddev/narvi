-- Queries backing ProviderCredentialStore (Step 53, "provider credential
-- injection", §25.1/§25.3) -- migrations/000056_provider_credentials.up.sql.
-- value_encrypted is ciphertext (platform.EncryptToken output) end to
-- end here; nothing in this file, or the Go store wrapping it, ever
-- decrypts -- that happens exactly once, server-side, in the sandbox-
-- facing delivery endpoint (internal/adapters/inbound/httpapi/
-- providercredentialsdelivery.go), only for the ONE row Resolve picked as
-- the winner.

-- name: CreateProviderCredential :one
-- scope_target_id is sqlc.narg: NULL for a global-scoped row (the CHECK
-- constraint provider_credentials_scope_target_id_shape enforces it can
-- ONLY be NULL for scope='global'), non-NULL otherwise. A duplicate
-- (scope, scope_target_id, provider) violates one of this table's own two
-- partial unique indexes -- the caller (httpapi.CreateProviderCredential)
-- maps that Postgres error into a 409 Conflict, never retried here.
INSERT INTO provider_credentials (scope, scope_target_id, provider, value_encrypted)
VALUES ($1, sqlc.narg('scope_target_id'), $2, $3)
RETURNING *;

-- name: GetProviderCredential :one
SELECT * FROM provider_credentials WHERE id = $1;

-- name: ListProviderCredentialsByScope :many
-- Lists every row at exactly one (scope, scope_target_id) pair -- e.g.
-- every provider configured for ONE repo, or every provider configured
-- for ONE environment, or (scope='global', scope_target_id NULL) every
-- global-scoped provider. scope_target_id is sqlc.narg (NULL for the
-- global case) with an explicit IS NOT DISTINCT FROM comparison, since a
-- plain "=" never matches NULL = NULL.
SELECT * FROM provider_credentials
WHERE scope = $1 AND scope_target_id IS NOT DISTINCT FROM sqlc.narg('scope_target_id')
ORDER BY provider;

-- name: UpdateProviderCredentialValue :one
-- Rotates ONLY the encrypted value -- scope/scope_target_id/provider are
-- immutable once created (see providercredentials.go's own doc comment
-- for why: changing what a row scopes to is modeled as delete-then-create,
-- never an in-place identity change).
UPDATE provider_credentials
SET value_encrypted = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteProviderCredential :execrows
DELETE FROM provider_credentials WHERE id = $1;

-- name: ListProviderCredentialsForResolution :many
-- The delivery endpoint's own single, session-scoped read: every
-- candidate row (across ALL 3 scopes, for EVERY provider at once) that
-- could possibly apply to one session -- the global row for each
-- provider (always included), any repo-scoped row for one of this
-- session's own repo_full_names, and the environment-scoped row for this
-- session's own environment_id, if any. repo_full_names may be an empty
-- array (matches nothing, never an error); environment_id is sqlc.narg,
-- NULL when the session has none (matches nothing -- scope_target_id is
-- NEVER NULL for an environment-scoped row, so "environment_id IS NULL"
-- correctly excludes every environment-scoped row when the caller has no
-- environment at all, rather than needing a separate branch). The caller
-- (providercredentialsdelivery.go) groups the result by provider and runs
-- internal/domain/providercredential.Resolve over each group.
SELECT * FROM provider_credentials
WHERE scope = 'global'
   OR (scope = 'repo' AND scope_target_id = ANY(sqlc.arg('repo_full_names')::text[]))
   OR (scope = 'environment' AND scope_target_id IS NOT NULL AND scope_target_id = sqlc.narg('environment_id'))
ORDER BY provider;
