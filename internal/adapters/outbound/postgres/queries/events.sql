-- Queries backing EventStore (§4.3, §6.1's append-only per-session event
-- log). CreateEvent appends; ListEventsForSession is the cursor-paginated
-- read this same table's own migration comment predicted ("reads
-- (cursor-paginated fetch_history) land with the client WS hub (Steps
-- 18+)") -- this is that Step, backing both the client WS hub's own
-- fetch_history/replay and the REST GET .../events endpoint (one
-- implementation, two callers).

-- name: CreateEvent :one
-- Upsert-on-(session_id, message_id) (§6.1: "receiver dedupes by
-- upsert-on-messageId") -- a resend of an already-seen messageId hits the
-- unique index (migrations/000019_events_message_id.up.sql) and this
-- DO UPDATE (a deliberate, self-referential no-op: type is set back to
-- its own current value) guarantees RETURNING always yields exactly one
-- row either way, so callers never need a separate "0 rows means
-- duplicate" branch. `(xmax = 0) AS inserted` is the standard Postgres
-- idiom for "was this row just inserted by THIS statement" (xmax is 0
-- only immediately after a fresh insert, non-zero after any update) --
-- callers use it to decide whether to (re-)broadcast this event to live
-- subscribers.
INSERT INTO events (session_id, type, message_id, payload) VALUES ($1, $2, $3, $4)
ON CONFLICT (session_id, message_id) DO UPDATE SET type = events.type
RETURNING *, (xmax = 0) AS inserted;

-- name: ListEventsForSession :many
-- afterID = 0 means "from the beginning" -- matches a null fetch_history
-- cursor / a REST ?cursor= default. The monotonic BIGSERIAL id is the
-- natural pagination cursor (events_session_id_id_idx,
-- migrations/000008_events.up.sql's own doc comment).
SELECT * FROM events
WHERE session_id = $1 AND id > $2
ORDER BY id ASC
LIMIT $3;

-- name: ListRecentEventsForSession :many
-- The mirror-image pagination direction from ListEventsForSession's own
-- oldest-first cursor page: returns up to $2 of session_id's own MOST
-- RECENT events, newest id first -- for a caller that needs only the TAIL
-- of a possibly-long event log (e.g. sessionactor.planContentText's own
-- best-effort plan-content extraction, Step 38) without scanning forward
-- from the very beginning of a session's entire history, which for a
-- long-lived session (many prior turns) could leave the CURRENT turn's
-- own events entirely outside a bounded oldest-first window. Same
-- events_session_id_id_idx index (migrations/000008_events.up.sql) serves
-- this DESC scan equally well.
SELECT * FROM events
WHERE session_id = $1
ORDER BY id DESC
LIMIT $2;
