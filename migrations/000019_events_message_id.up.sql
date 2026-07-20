-- events.message_id: the wire event's own top-level "messageId" (every
-- one of the 19 sandbox-ws event types requires it,
-- contracts/sandbox-ws/v1/events.schema.json), carried through so a
-- sandbox resending an event whose ack was lost (wshub's own readLoop
-- tolerates a missed ack by design, see internal/adapters/inbound/wshub/
-- dispatch.go) can be deduped rather than persisted as an indistinguishable
-- duplicate row -- §6.1's "receiver dedupes by upsert-on-messageId".
--
-- This is a young, pre-production schema (no real deployed data yet), so
-- a straight NOT NULL add needs no backfill dance, matching this
-- project's own precedent for prior NOT NULL column additions (e.g.
-- migrations/000018_session_repos.up.sql's plan_mode/spawn_failure_count).
--
-- UNIQUE (session_id, message_id) is the dedupe key itself: a single
-- session can never legitimately see the same messageId twice from two
-- DIFFERENT genuine events, so this is a plain unique index, not a
-- composite business key shared with anything else -- matching this
-- table's own existing events_session_id_id_idx naming convention.
ALTER TABLE events ADD COLUMN message_id TEXT NOT NULL;

CREATE UNIQUE INDEX events_session_id_message_id_idx ON events (session_id, message_id);
