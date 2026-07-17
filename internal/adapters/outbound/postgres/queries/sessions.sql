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

-- name: BumpActorEpoch :one
-- Increments a session's actor_epoch and returns the new value. Called
-- once, at acquisition time, when an actor takes ownership of a session
-- (§2: "bumped on each acquisition") -- never as part of an ordinary
-- state-transition write (those only READ the epoch, via
-- GetSessionActorEpochForUpdate below, to check it hasn't moved).
UPDATE sessions SET actor_epoch = actor_epoch + 1
WHERE id = $1
RETURNING actor_epoch;

-- name: GetSessionActorEpochForUpdate :one
-- Locks and reads a session's current actor_epoch. Called at the START of
-- every transactional write, inside that same transaction, to fence a
-- stale writer (an actor whose epoch no longer matches -- proof a newer
-- actor has since taken over) before it does anything else (§2: "writes
-- with a stale epoch fail").
SELECT actor_epoch FROM sessions
WHERE id = $1
FOR UPDATE;

-- name: UpdateSessionStatus :one
-- Persists a session's derived status + failure_reason --
-- internal/domain/session.DeriveStatus's output, never written directly
-- (§11: "every state transition goes through the machine's transition
-- table").
UPDATE sessions
SET status = $2, failure_reason = $3, updated_at = now()
WHERE id = $1
RETURNING *;
