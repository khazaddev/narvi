-- Queries backing ClusterBindingStore ("cloud identity:
-- sandbox-side consumption + kubeconfig injection", §27.4) --
-- migrations/000094_cluster_bindings.up.sql. params travels as opaque
-- JSONB bytes end to end here -- identifiers, never secrets (this
-- migration's own top comment), mirroring cloud_identity_bindings.sql's
-- own identical "no decrypt step anywhere in this table's own read path"
-- shape.
--
-- environment_id is a single, mandatory column (not a polymorphic
-- scope/scope_target_id pair) -- v1 has no global fallback at all (this
-- table's own migration doc comment), so there is exactly ONE arbiter
-- index (cluster_bindings_environment_id_uniq) for Upsert to target,
-- unlike opencode_configs.sql's own two-scope split.

-- name: UpsertClusterBinding :one
-- Create-or-replace for the (at most one) cluster_bindings row belonging
-- to one Environment -- backs PUT /api/environments/{environmentID}/
-- cluster-binding.
INSERT INTO cluster_bindings (environment_id, name, server_url, ca_bundle, auth_kind, params)
VALUES ($1, $2, sqlc.narg('server_url'), sqlc.narg('ca_bundle'), $3, $4)
ON CONFLICT (environment_id)
DO UPDATE SET name = EXCLUDED.name, server_url = EXCLUDED.server_url, ca_bundle = EXCLUDED.ca_bundle,
               auth_kind = EXCLUDED.auth_kind, params = EXCLUDED.params, updated_at = now()
RETURNING *;

-- name: GetClusterBinding :one
-- pgx.ErrNoRows means "no cluster configured for this Environment yet"
-- -- an ordinary, expected state (§27.4's own "at most one... per
-- environment", never "exactly one"), not an error condition; the
-- httpapi GET handler renders this as 404.
SELECT * FROM cluster_bindings WHERE environment_id = $1;

-- name: DeleteClusterBinding :execrows
DELETE FROM cluster_bindings WHERE environment_id = $1;
