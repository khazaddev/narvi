-- slack_thread_sessions: the thread<->session mapping Step 33 ("Slack
-- ingress", §8.10 "thread↔session mapping") needs so that a REPLY within
-- an already-mapped Slack thread continues the SAME session (a new turn)
-- instead of spawning a duplicate one, while the FIRST mention in a
-- brand-new thread creates the mapping.
--
-- Keyed by (channel_id, thread_ts) -- Slack's own real, stable identity
-- for "one thread" (a channel id plus the root message's own `ts`; every
-- reply's own event carries that SAME thread_ts back, per Slack's
-- Events API/message shape). session_id is NOT NULL and REFERENCES
-- sessions(id): unlike webhook_deliveries' own claim-before-process
-- table (migrations/000027_webhook_deliveries.up.sql), a genuinely
-- concurrency-safe claim here does not need a nullable "reservation, no
-- payload yet" row -- internal/adapters/inbound/slack's own handler
-- always creates the session FIRST (via httpapi.CreateSessionCore, with
-- no prompt yet, so it never dispatches -- see that package's own doc.go
-- for the full "optimistic create, then atomic claim, discard the loser"
-- sequencing this table's own INSERT ... ON CONFLICT DO NOTHING claim
-- enables), so a real session_id already exists by the time this row is
-- ever inserted.
--
-- The composite PRIMARY KEY is the dedupe mechanism -- exactly
-- webhook_deliveries' own house style (§5.1: "Dedupe/coalescing ...
-- via INSERT ... ON CONFLICT atomic claims"): two near-simultaneous
-- first messages in the SAME brand-new thread race to INSERT the same
-- (channel_id, thread_ts); the DB's own uniqueness constraint -- not a
-- race-prone read-then-write check in application code -- guarantees
-- exactly one of them wins and every other racer's own freshly-created,
-- never-dispatched session is simply discarded (see the adapter's own
-- doc comment for why that discard is safe/side-effect-free).
CREATE TABLE slack_thread_sessions (
    channel_id  TEXT NOT NULL,
    thread_ts   TEXT NOT NULL,
    session_id  UUID NOT NULL REFERENCES sessions(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, thread_ts)
);

-- A reply's own claim/turn-creation path looks up by session_id in the
-- other direction too (not needed for the core mapping lookup itself,
-- which is always by the PRIMARY KEY above, but kept for symmetry with
-- every other FK this schema already indexes and for any future
-- "which thread does this session belong to" lookup, e.g. posting the
-- in-thread ack back to the right channel/thread from session context
-- alone).
CREATE INDEX slack_thread_sessions_session_id_idx ON slack_thread_sessions (session_id);
