-- Queries backing SessionStore (§4.3). Just enough to prove the pipeline
-- end to end (create + get) — full CRUD lands with the PRs that build out
-- session-actor persistence (PR-11+).

-- name: CreateSession :one
-- repos defaults to '[]'::jsonb via COALESCE (not the column's own DEFAULT
-- clause) specifically so every EXISTING call site that never set Repos
-- (every session created before Step 21 "e2e happy path") keeps compiling
-- and behaving identically: a nil/absent []byte param binds SQL NULL, and
-- COALESCE(NULL, '[]'::jsonb) resolves to the same empty-list default a
-- bare column-default insert would have produced.
--
-- environment_id/provenance_tag (row 10, "domain: Environment scoping",
-- §14.1) are both sqlc.narg -- nullable, optional params -- so every
-- EXISTING call site that never sets them (every session created before
-- this batch) keeps compiling and behaving identically: both stay NULL,
-- byte-for-byte today's unscoped behavior.
--
-- build_model_id (Step 37, "plan mode, web", §12.2 item 3) is likewise
-- sqlc.narg -- every EXISTING call site that never sets it keeps
-- compiling and behaving identically (NULL, "use the default model
-- catalog entry", migrations/000034_plan_mode.up.sql's own convention).
--
-- parent_session_id/spawn_depth (Step 48, "sentinels + suggestions",
-- §17.2, migrations/000045) are likewise sqlc.narg/COALESCE-defaulted --
-- every EXISTING call site (every session created before this Step) keeps
-- compiling and behaving identically: parent_session_id stays NULL,
-- spawn_depth stays 0. httpapi.SpawnChildSession (childsession.go) is this
-- Step's own one real caller that supplies non-default values.
INSERT INTO sessions (title, spawn_source, created_by, repos, environment_id, provenance_tag, build_model_id, parent_session_id, spawn_depth)
VALUES ($1, $2, $3, COALESCE(sqlc.narg('repos'), '[]'::jsonb), sqlc.narg('environment_id'), sqlc.narg('provenance_tag'), sqlc.narg('build_model_id'), sqlc.narg('parent_session_id'), COALESCE(sqlc.narg('spawn_depth'), 0))
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

-- name: UpdateSessionConversationID :one
-- Persists the session-level OpenCode conversation id (§3.3: "recorded at
-- turn start ... also reported on every heartbeat"; see
-- migrations/000018_session_repos.up.sql's own doc comment for why this is
-- session-scoped, not turns.conversation_id). Called by
-- internal/app/sessionactor's handleSandboxEvent whenever a "heartbeat"
-- event carries a non-nil ConversationId.
UPDATE sessions
SET opencode_conversation_id = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: UpdateSessionIntentDecisionIfNull :execrows
-- Step 36's ("intent classifier", §18.4) write-once guarded update:
-- "UPDATE sessions SET intent_decision = ... WHERE intent_decision IS
-- NULL" -- NOT read-then-write, first decision wins, no application-level
-- lock needed. RowsAffected (via :execrows) is the caller's own win/lose
-- signal: 1 means THIS call actually set intent_decision (first writer
-- wins); 0 means some other writer already set it first -- internal/app/
-- intentclassifier's persistence path treats either outcome as success,
-- never an error, since "someone already recorded a decision for this
-- session" is exactly the expected, race-safe steady state, not a
-- failure.
UPDATE sessions
SET intent_decision = $2
WHERE id = $1 AND intent_decision IS NULL;
