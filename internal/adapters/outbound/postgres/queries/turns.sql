-- Queries backing TurnStore (§4.3). Just enough to prove the pipeline end
-- to end (create + get), including exercising the
-- turns_one_processing_per_session partial unique index (§3.3).

-- name: CreateTurn :one
-- prompt/model_id/plan_mode (migrations/000018_session_repos.up.sql, Step
-- 21) are the turn's own dispatch-time inputs -- prompt/model_id are
-- nullable and plan_mode defaults false, so every EXISTING call site
-- (every prior Step's `CreateTurnParams{SessionID, Status}`) keeps
-- compiling and behaving identically: the zero-value nil/nil/false it
-- already implicitly got before this Step's own columns existed.
INSERT INTO turns (session_id, status, prompt, model_id, plan_mode)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetTurn :one
SELECT * FROM turns
WHERE id = $1;

-- name: UpdateTurnStatus :one
-- Sets a turn's status, plus dispatched_at/completed_at/
-- dispatched_sandbox_gen when the caller supplies one (sqlc.narg +
-- COALESCE: an absent/NULL argument leaves the existing column value
-- untouched, matching dispatched_at/completed_at's own nullability --
-- each is set at most once, at the (from, trigger) transition that
-- reaches Dispatched or a terminal state respectively).
--
-- dispatched_sandbox_gen (migration 000026_turn_dispatch_gen.up.sql, Step
-- 28 "turn recovery") is stamped by TWO distinct call sites sharing this
-- SAME query, both at the moment a Prompt payload is built and about to be
-- sent: tryPlanDispatch (internal/app/sessionactor/dispatch.go), alongside
-- the SAME status=dispatched write that already sets dispatched_at, for
-- the normal Pending->Dispatched->Processing path; and tryPlanReenqueue
-- (same file), which passes the CURRENT status back unchanged (the turn
-- is already, validly, Processing -- this call re-stamps
-- dispatched_sandbox_gen only, never re-transitions status) for a
-- Processing turn whose prompt needs re-sending to a respawned sandbox
-- incarnation.
UPDATE turns
SET status = $2,
    dispatched_at = COALESCE(sqlc.narg('dispatched_at'), dispatched_at),
    completed_at = COALESCE(sqlc.narg('completed_at'), completed_at),
    dispatched_sandbox_gen = COALESCE(sqlc.narg('dispatched_sandbox_gen'), dispatched_sandbox_gen)
WHERE id = $1
RETURNING *;

-- name: ListTurnsForSession :many
-- Full turn history for one session, oldest first -- exactly the input
-- shape internal/domain/session.DeriveStatus requires (an ordered slice
-- of turn.Summary derived from these rows).
SELECT * FROM turns
WHERE session_id = $1
ORDER BY created_at ASC;
