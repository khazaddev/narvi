-- Queries backing EventStore (§4.3, §6.1's append-only per-session event
-- log). Just CreateEvent for now -- reads (cursor-paginated fetch_history)
-- land with the client WS hub (Steps 18+).

-- name: CreateEvent :one
INSERT INTO events (session_id, type, payload) VALUES ($1, $2, $3)
RETURNING *;
