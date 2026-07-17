-- Queries backing SandboxStore (§4.3). Just enough to prove the pipeline
-- end to end (create + get) — the UNIQUE(session_id) constraint (§3.2) is
-- exercised by the integration test, not by these queries.

-- name: CreateSandbox :one
INSERT INTO sandboxes (session_id)
VALUES ($1)
RETURNING *;

-- name: GetSandbox :one
SELECT * FROM sandboxes
WHERE session_id = $1;
