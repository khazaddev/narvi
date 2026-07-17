-- Queries backing TurnStore (§4.3). Just enough to prove the pipeline end
-- to end (create + get), including exercising the
-- turns_one_processing_per_session partial unique index (§3.3).

-- name: CreateTurn :one
INSERT INTO turns (session_id, status)
VALUES ($1, $2)
RETURNING *;

-- name: GetTurn :one
SELECT * FROM turns
WHERE id = $1;

-- name: UpdateTurnStatus :one
-- Sets a turn's status, plus dispatched_at/completed_at when the caller
-- supplies one (sqlc.narg + COALESCE: an absent/NULL argument leaves the
-- existing column value untouched, matching dispatched_at/completed_at's
-- own nullability -- each is set at most once, at the (from, trigger)
-- transition that reaches Dispatched or a terminal state respectively).
UPDATE turns
SET status = $2,
    dispatched_at = COALESCE(sqlc.narg('dispatched_at'), dispatched_at),
    completed_at = COALESCE(sqlc.narg('completed_at'), completed_at)
WHERE id = $1
RETURNING *;

-- name: ListTurnsForSession :many
-- Full turn history for one session, oldest first -- exactly the input
-- shape internal/domain/session.DeriveStatus requires (an ordered slice
-- of turn.Summary derived from these rows).
SELECT * FROM turns
WHERE session_id = $1
ORDER BY created_at ASC;
