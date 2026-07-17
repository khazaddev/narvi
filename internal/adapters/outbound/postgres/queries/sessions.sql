-- Queries backing SessionStore (§4.3). Just enough to prove the pipeline
-- end to end (create + get) — full CRUD lands with the PRs that build out
-- session-actor persistence (PR-11+).

-- name: CreateSession :one
INSERT INTO sessions (title, spawn_source, created_by)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetSession :one
SELECT * FROM sessions
WHERE id = $1;
