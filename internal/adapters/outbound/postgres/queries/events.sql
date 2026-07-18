-- Queries backing EventStore (§4.3, §6.1's append-only per-session event
-- log). CreateEvent appends; ListEventsForSession is the cursor-paginated
-- read this same table's own migration comment predicted ("reads
-- (cursor-paginated fetch_history) land with the client WS hub (Steps
-- 18+)") -- this is that Step, backing both the client WS hub's own
-- fetch_history/replay and the REST GET .../events endpoint (one
-- implementation, two callers).

-- name: CreateEvent :one
INSERT INTO events (session_id, type, payload) VALUES ($1, $2, $3)
RETURNING *;

-- name: ListEventsForSession :many
-- afterID = 0 means "from the beginning" -- matches a null fetch_history
-- cursor / a REST ?cursor= default. The monotonic BIGSERIAL id is the
-- natural pagination cursor (events_session_id_id_idx,
-- migrations/000008_events.up.sql's own doc comment).
SELECT * FROM events
WHERE session_id = $1 AND id > $2
ORDER BY id ASC
LIMIT $3;
