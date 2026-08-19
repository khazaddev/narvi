-- Queries backing CloudIdentityBindingStore (Step 73a, "cloud identity:
-- OIDC issuer, bindings, minting", §27.3) --
-- migrations/000093_cloud_identity_bindings.up.sql. params travels as
-- opaque JSONB bytes end to end here -- identifiers, never secrets (this
-- migration's own top comment), so unlike provider_credentials/
-- sandbox_secrets there is no decrypt step anywhere in this table's own
-- read path.

-- name: CreateCloudIdentityBinding :one
-- scope_target_id is sqlc.narg: NULL for a global-scoped row (the CHECK
-- constraint cloud_identity_bindings_scope_target_id_shape enforces it can
-- ONLY be NULL for scope='global'), non-NULL otherwise. A duplicate
-- (scope, scope_target_id, kind) violates one of this table's own two
-- partial unique indexes; kind='azure' at scope='global' violates
-- cloud_identity_bindings_no_azure_global -- the caller
-- (httpapi.CreateCloudIdentityBinding) validates both up front
-- (internal/domain/cloudidentity.ValidateBinding) and maps either
-- Postgres-level rejection into the same response shape defensively, but
-- neither should ever actually reach this INSERT once that Go-layer
-- validation runs first.
INSERT INTO cloud_identity_bindings (scope, scope_target_id, kind, audience, params)
VALUES ($1, sqlc.narg('scope_target_id'), $2, $3, $4)
RETURNING *;

-- name: GetCloudIdentityBinding :one
SELECT * FROM cloud_identity_bindings WHERE id = $1;

-- name: ListCloudIdentityBindingsByScope :many
-- Lists every row at exactly one (scope, scope_target_id) pair -- e.g.
-- every binding configured for ONE environment (up to 4, one per kind),
-- or (scope='global', scope_target_id NULL) every global-scoped binding.
-- Mirrors ListProviderCredentialsByScope/ListSandboxSecretsByScope's own
-- identical shape.
SELECT * FROM cloud_identity_bindings
WHERE scope = $1 AND scope_target_id IS NOT DISTINCT FROM sqlc.narg('scope_target_id')
ORDER BY kind;

-- name: UpdateCloudIdentityBinding :one
-- Rotates ONLY audience/params -- scope/scope_target_id/kind are
-- immutable once created (delete-then-create if a different scope/
-- target/kind is actually wanted), mirroring UpdateProviderCredentialValue
-- /UpdateSandboxSecretValue's own identical "identity fields immutable,
-- payload fields rotate in place" posture.
UPDATE cloud_identity_bindings
SET audience = $2, params = $3, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteCloudIdentityBinding :execrows
DELETE FROM cloud_identity_bindings WHERE id = $1;

-- name: ListCloudIdentityBindingsForResolution :many
-- The minting endpoint's own single, session-scoped read: every candidate
-- binding (global, plus this session's own environment_id if it has one)
-- whose audience matches the requested one -- at most 2 rows back in
-- practice (one environment-scoped, one global-scoped, per kind, but only
-- kinds whose audience happens to match the request are relevant, and
-- v1's own "at most one per (scope target, kind)" rule bounds this
-- further). environment_id is sqlc.narg, NULL when the session has none
-- (matches nothing at that scope, never a wildcard -- scope_target_id is
-- NEVER NULL for an environment-scoped row, so "IS NULL" correctly
-- excludes every row of that scope when the caller has none at all,
-- mirroring ListProviderCredentialsForResolution's own identical
-- environmentID convention). The caller (cloudidentitytoken.go) groups
-- the result via internal/domain/providercredential.Resolve to confirm at
-- least one candidate exists and to pick a deterministic winner for
-- observability -- the token's own claims (sub/aud/exp) are identical
-- regardless of which candidate wins, since kind/params never appear in
-- a claim (see internal/domain/cloudidentity's own doc comment).
SELECT * FROM cloud_identity_bindings
WHERE audience = sqlc.arg('audience')
  AND (scope = 'global'
   OR (scope = 'environment' AND scope_target_id IS NOT NULL AND scope_target_id = sqlc.narg('environment_id')))
ORDER BY kind;

-- name: ListCloudIdentityBindingsForSession :many
-- Step 73b's own ("cloud identity: sandbox-side consumption + kubeconfig
-- injection", §27.3) sandbox-facing delivery endpoint's own single,
-- session-scoped read: EVERY candidate binding (global, plus this
-- session's own environment_id if it has one) regardless of audience --
-- unlike ListCloudIdentityBindingsForResolution (above), which filters to
-- one REQUESTED audience for the minting endpoint's own allowlist check,
-- this query answers a DIFFERENT question sandbox-agent asks at boot:
-- "which bindings apply to my session at all, so I know which kinds to
-- prepare a token file for and what audience/params each one declares" --
-- the caller (httpapi's own cloud-identity-config delivery handler) then
-- groups the result by Kind and resolves environment-vs-global via
-- internal/domain/providercredential.Resolve, mirroring
-- resolveCloudIdentityBindingForAudience's own identical resolution
-- shape, just without the audience pre-filter. environment_id is
-- sqlc.narg, NULL when the session has none (matches nothing at that
-- scope, never a wildcard -- mirrors ListCloudIdentityBindingsForResolution's
-- own identical environment_id convention).
SELECT * FROM cloud_identity_bindings
WHERE scope = 'global'
   OR (scope = 'environment' AND scope_target_id IS NOT NULL AND scope_target_id = sqlc.narg('environment_id'))
ORDER BY kind;
