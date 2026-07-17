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
