-- Queries backing LinearAgentSessionStore (Step 34, "Linear ingress",
-- §8.10 -- migrations/000028_linear_agent_sessions.up.sql's own doc
-- comment has the full "why this table exists at all" writeup).

-- name: ClaimLinearAgentSession :one
-- Atomic first-writer-wins claim on agent_session_id -- the SAME
-- "(xmax = 0) AS inserted" idiom ClaimWebhookDelivery already establishes
-- (postgres/queries/webhookdeliveries.sql), reused here for a DIFFERENT
-- identity (Linear's own AgentSession id, not a webhook delivery id).
-- Inserted=true means THIS call is the one that gets to actually create
-- the Narvi session (SetSessionID below, once createSessionCore returns);
-- Inserted=false means another `created` event for the SAME agent session
-- already claimed it first -- the caller must never create a second
-- session, regardless of whether that earlier claim has finished
-- attaching its own session_id yet.
INSERT INTO linear_agent_sessions (agent_session_id, organization_id)
VALUES ($1, $2)
ON CONFLICT (agent_session_id) DO UPDATE SET agent_session_id = linear_agent_sessions.agent_session_id
RETURNING *, (xmax = 0) AS inserted;

-- name: SetLinearAgentSessionSessionID :exec
-- Attaches the real Narvi session id to a row this same request just won
-- the claim on (ClaimLinearAgentSession's Inserted=true branch) -- run
-- AFTER createSessionCore's own transaction has already committed, so a
-- createSessionCore failure never leaves a claimed agent_session_id
-- pointing at a session that doesn't exist.
UPDATE linear_agent_sessions
SET session_id = $2
WHERE agent_session_id = $1;

-- name: GetLinearAgentSessionByAgentSessionID :one
-- The `prompted`-event lookup: which Narvi session (if any) does this
-- Linear agent session already back? A pgx.ErrNoRows result, OR a row
-- whose session_id is still NULL (the `created` claim won by a
-- still-in-flight createSessionCore call), both mean "nothing to route
-- this prompt to yet" -- the caller treats both the same way (log + ack,
-- never fabricate a session).
SELECT * FROM linear_agent_sessions
WHERE agent_session_id = $1;
