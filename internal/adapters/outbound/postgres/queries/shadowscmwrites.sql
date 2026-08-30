-- name: CreateShadowSCMWrite :one
-- §30.6's record-or-fail insert: the gate returns success to its caller
-- ONLY if this commits. A suppressed-but-unrecorded effect is a contract
-- violation, and failing loudly is safe -- nothing external happened, so
-- there is nothing to reconcile.
--
-- heavy_content (migrations/000110_shadow_scm_writes_heavy_content.up.sql)
-- is NULL for every operation except update_file_content -- see that
-- migration's own doc comment, and shadowledger.Record (writer.go),
-- the one call site that ever passes a non-NULL value here.
INSERT INTO shadow_scm_writes (
    operation, repo_full_name, target, spec_json, result_json, session_id, correlation_id, heavy_content
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: ListShadowSCMWritesForRepo :many
-- Newest first, the order the operator surface reads them in.
SELECT * FROM shadow_scm_writes
WHERE repo_full_name = $1
ORDER BY created_at DESC, id DESC
LIMIT $2;
