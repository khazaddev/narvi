-- Queries backing SandboxSecretStore ("sandbox secrets &
-- opencode config", §27.1) -- migrations/000090_sandbox_secrets.up.sql.
-- value_encrypted is ciphertext (platform.EncryptToken output) end to end
-- here; nothing in this file, or the Go store wrapping it, ever decrypts
-- -- that happens exactly once, server-side, in the sandbox-facing
-- delivery endpoint (internal/adapters/inbound/httpapi/
-- sandboxsecretsdelivery.go), only for the winning row(s)
-- providercredential.Resolve picks per name.

-- name: CreateSandboxSecret :one
-- scope_target_id is sqlc.narg: NULL for a global-scoped row (the CHECK
-- constraint sandbox_secrets_scope_target_id_shape enforces it can ONLY
-- be NULL for scope='global'), non-NULL otherwise. A duplicate (scope,
-- scope_target_id, name) violates one of this table's own two partial
-- unique indexes -- the caller (httpapi.CreateSandboxSecret) maps that
-- Postgres error into a 409 Conflict, never retried here.
INSERT INTO sandbox_secrets (scope, scope_target_id, name, value_encrypted)
VALUES ($1, sqlc.narg('scope_target_id'), $2, $3)
RETURNING *;

-- name: GetSandboxSecret :one
SELECT * FROM sandbox_secrets WHERE id = $1;

-- name: ListSandboxSecretsByScope :many
-- Lists every row at exactly one (scope, scope_target_id) pair -- e.g.
-- every secret configured for ONE repo, or every secret configured for
-- ONE environment, or (scope='global', scope_target_id NULL) every
-- global-scoped secret. scope_target_id is sqlc.narg (NULL for the global
-- case) with an explicit IS NOT DISTINCT FROM comparison, since a plain
-- "=" never matches NULL = NULL.
SELECT * FROM sandbox_secrets
WHERE scope = $1 AND scope_target_id IS NOT DISTINCT FROM sqlc.narg('scope_target_id')
ORDER BY name;

-- name: UpdateSandboxSecretValue :one
-- Rotates ONLY the encrypted value -- scope/scope_target_id/name are
-- immutable once created (delete-then-create if a different scope/
-- target/name is actually wanted), mirroring
-- UpdateProviderCredentialValue's own identical posture.
UPDATE sandbox_secrets
SET value_encrypted = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteSandboxSecret :execrows
DELETE FROM sandbox_secrets WHERE id = $1;

-- name: ListSandboxSecretsForResolution :many
-- The delivery endpoint's own single, session-scoped read: every
-- candidate row (across every scope THIS Step actually resolves, for
-- EVERY name at once) that could possibly apply to one session -- the
-- global row for each name (always included), any repo-scoped row for
-- one of this session's own repo_full_names, and the environment-scoped
-- row for this session's own environment_id (if any). Deliberately NO
-- 'automation' branch -- §27.1's own schema-only carve-out for that scope
-- (see migrations/000090_sandbox_secrets.up.sql's own top comment):
-- nothing calls this query with an automation id to match against, so an
-- automation-scoped row (should one ever exist) is simply never a
-- candidate, exactly as if it were absent. repo_full_names may be an
-- empty array (matches nothing, never an error); environment_id is
-- sqlc.narg, NULL when the session has none (matches nothing --
-- scope_target_id is NEVER NULL for an environment-scoped row, so "IS
-- NULL" correctly excludes every row of that scope when the caller has
-- none at all, mirroring ListProviderCredentialsForResolution's own
-- identical userID/environmentID convention). The caller
-- (sandboxsecretsdelivery.go) groups the result by name and runs
-- internal/domain/providercredential.Resolve over each group.
SELECT * FROM sandbox_secrets
WHERE scope = 'global'
   OR (scope = 'repo' AND scope_target_id = ANY(sqlc.arg('repo_full_names')::text[]))
   OR (scope = 'environment' AND scope_target_id IS NOT NULL AND scope_target_id = sqlc.narg('environment_id'))
ORDER BY name;
