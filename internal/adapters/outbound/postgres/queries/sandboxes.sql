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

-- name: UpdateSandboxStatus :one
-- Sets a sandbox's status, plus last_seen_at when the caller supplies a
-- real timestamp (sqlc.narg + COALESCE, same pattern as
-- UpdateTurnStatus) -- per §3.2 "Liveness = max of all signals",
-- last_seen_at only ever moves forward on an actual signal, never as a
-- side effect of a plain status write.
UPDATE sandboxes
SET status = $2,
    last_seen_at = COALESCE(sqlc.narg('last_seen_at'), last_seen_at),
    updated_at = now()
WHERE session_id = $1
RETURNING *;
