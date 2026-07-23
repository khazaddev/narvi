-- linear_agent_sessions: maps Linear's own AgentSession identity (its
-- "agentSession.id", a UUID Linear itself mints and never reuses -- see
-- schema.graphql's AgentSessionWebhookPayload.id) to the Narvi session it
-- backs (Step 34, "Linear ingress", §8.10).
--
-- Design note: unlike GitHub (a PR can accumulate many concurrent
-- @mention events needing coalescing) or Slack (a thread has no single
-- provider-minted "this is one unit of work" id of its own), Linear's own
-- AgentSession IS ALREADY that 1:1-with-one-unit-of-work identity --
-- Linear mints exactly one `created` AgentSessionEvent per session, ever
-- (confirmed against Linear's real docs during this Step's investigation:
-- "An AgentSession is created automatically when an agent is mentioned or
-- delegated an issue"). So this table is NOT a general coalescing/claim
-- table like GitHub's future per-PR-mention one; it exists purely to
-- answer two questions cheaply and atomically: (1) "have we already
-- created a Narvi session for this Linear agent session?" (a `created`
-- event redelivered, or Linear somehow double-firing) and (2) "which
-- Narvi session does this `prompted` event's agent_session_id belong to?"
--
-- session_id is nullable so the (agent_session_id) identity can be
-- CLAIMED atomically (INSERT ... ON CONFLICT, the same first-writer-wins
-- idiom migrations/000027_webhook_deliveries.up.sql's own doc comment
-- explains) BEFORE the real, possibly-slower session-creation work
-- (createSessionCore: repo validation, the transactional session+turn
-- insert, the post-commit actor dispatch) runs -- closing the race where
-- two concurrent deliveries for the SAME brand-new agent session both
-- pass the webhook_deliveries dedupe (e.g. two genuinely distinct
-- Linear-Delivery ids for what Linear itself considers one logical
-- event) and both attempt to create a session. The LOSER of the
-- agent_session_id claim below never runs createSessionCore at all, so
-- at most one Narvi session is ever created per Linear agent session,
-- even under that race -- the row's session_id is filled in by an UPDATE
-- once the winner's createSessionCore call actually returns a session id.
CREATE TABLE linear_agent_sessions (
    agent_session_id TEXT PRIMARY KEY,
    session_id       UUID REFERENCES sessions(id) ON DELETE CASCADE,
    organization_id  TEXT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Reverse lookup an existing mapping's own Narvi session (e.g. future
-- admin/debug tooling); the hot ingress-path lookup itself is keyed by
-- agent_session_id (the primary key) and needs no separate index.
CREATE INDEX linear_agent_sessions_session_id_idx ON linear_agent_sessions (session_id);
