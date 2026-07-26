-- Queries backing LinearAgentSessionStore (Step 34, "Linear ingress",
-- §8.10 -- migrations/000030_linear_agent_sessions.up.sql's own doc
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

-- name: GetLinearAgentSessionBySessionID :one
-- The REVERSE lookup Step 35 ("outbox delivery") needs: given a
-- session_id, which agent_session_id/organization_id does it back? Backed
-- by migrations/000030_linear_agent_sessions.up.sql's own already-existing
-- linear_agent_sessions_session_id_idx (Step 34 added this index up
-- front). A pgx.ErrNoRows result means this session was never created via
-- a Linear agent session -- the caller skips enqueuing a Linear
-- notification entirely rather than fabricating one.
SELECT * FROM linear_agent_sessions
WHERE session_id = $1;

-- name: ReleaseLinearAgentSessionClaim :exec
-- Audit-fix addition (H3, "webhook claim/release parity"): un-claims a
-- first-writer-wins row this same request just won via
-- ClaimLinearAgentSession (Inserted=true), but failed to actually
-- complete -- an authorization denial, a CreateSessionCore error, a
-- SetSessionID error, or a crash between the claim's own commit and the
-- follow-up SetSessionID call (these are NOT one transaction -- see
-- migrations/000030_linear_agent_sessions.up.sql's own doc comment).
-- Without this, ANY of those leaves the row permanently stuck with a NULL
-- session_id: every future prompt/redelivery for that agent_session_id is
-- dropped forever as "still being claimed" (webhook.go's own
-- handlePrompted), with no recovery short of DB surgery -- mirrors
-- ReleaseWebhookDelivery's own reasoning (postgres/queries/
-- webhookdeliveries.sql) for a DIFFERENT claim identity.
--
-- The `session_id IS NULL` guard is load-bearing: a row that already has
-- a REAL session_id attached backs a session that genuinely exists (and
-- may already be in progress) -- releasing THAT row would let a later,
-- unrelated event re-claim the SAME agent_session_id and attempt to
-- create a SECOND, colliding session, exactly the coalescing failure this
-- table exists to prevent (migrations/000030_linear_agent_sessions.up.sql's
-- own doc comment). Scoping the DELETE to session_id IS NULL makes this
-- safe to call unconditionally on every post-claim failure branch: if
-- SetSessionID already won before this call runs, the DELETE simply
-- matches zero rows, and the real, already-attached session is left
-- completely untouched.
DELETE FROM linear_agent_sessions WHERE agent_session_id = $1 AND session_id IS NULL;
