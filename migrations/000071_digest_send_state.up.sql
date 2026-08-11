-- digest_send_state (Step 62, §21.3): claim-before-act guarantee of
-- at-most-one deterministic daily digest send per (send_date, channel)
-- under concurrent pump ticks (internal/app/digest.Pump -- multiple
-- control-plane pods, or two overlapping ticks of the same pod).
--
-- Two-phase design, mirroring the outbox's own established
-- claim-before-network-call discipline (queries/outbox.sql,
-- ListDuePendingOutboxEntries/ClaimOutboxEntry) rather than a single
-- INSERT...ON CONFLICT claim:
--
--  1. SEED (idempotent): internal/app/digest.Pump discovers candidate
--     (send_date=today, channel_provider, channel_id) triples from
--     EXISTING session-thread association tables (slack_thread_sessions/
--     linear_agent_sessions, joined through github_pr_sessions -- never a
--     second, separate repo<->channel mechanism, §21.3) and INSERTs a
--     'pending' row for each, ON CONFLICT (send_date, channel_provider,
--     channel_id) DO NOTHING -- safe under concurrent ticks: whichever
--     tick's INSERT lands first wins, every other tick's own identical
--     INSERT is a harmless no-op.
--  2. CLAIM: SELECT ... WHERE status = 'pending' FOR UPDATE SKIP LOCKED,
--     then UPDATE that same row's status to 'sending' BEFORE the actual
--     Slack/Linear network send is attempted (via the outbox, exactly
--     like every other outbound notification in this codebase, §5.1) --
--     the row transitioning out of 'pending' IS the at-most-one-send
--     guarantee: two concurrent ticks racing the SAME row's SELECT ...
--     FOR UPDATE SKIP LOCKED can never BOTH see it as claimable, since
--     SKIP LOCKED makes the second ticket's own scan silently pass over a
--     row the first ticket is still holding a row lock on.
--
-- channel_provider/channel_id are the SAME generic "which external
-- destination" pair reused throughout: channel_provider is 'slack' or
-- 'linear' (mirrors identities.provider's own vocabulary, though this
-- column is plain TEXT -- the closed vocabulary lives in Go,
-- internal/domain/digest, matching review_findings.sentinel_kind's own
-- established "schema stays agnostic, sibling Go package is the source
-- of truth" precedent, migrations/000046); channel_id is a Slack channel
-- ID (slack_thread_sessions.channel_id) or a Linear organization_id
-- (linear_agent_sessions.organization_id -- Linear has no distinct
-- "channel"/"team" concept in this schema, see this migration's own PR
-- description for why organization_id is the closest existing
-- granularity, reused rather than inventing a new one).
--
-- send_date is a DATE, not a TIMESTAMPTZ -- "one digest per channel per
-- CALENDAR day" is the unit §21.3 states directly ("at-most-one send per
-- channel per day"), computed once by the pump from the CONTROL PLANE's
-- own configured reporting timezone (never per-recipient, avoiding a
-- combinatorial "which of a channel's many members' own timezones wins"
-- question this Step does not need to answer).
CREATE TYPE digest_send_status AS ENUM ('pending', 'sending', 'sent', 'failed');

CREATE TABLE digest_send_state (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    send_date        DATE NOT NULL,
    channel_provider TEXT NOT NULL,
    channel_id       TEXT NOT NULL,
    status           digest_send_status NOT NULL DEFAULT 'pending',
    claimed_at       TIMESTAMPTZ,
    sent_at          TIMESTAMPTZ,
    last_error       TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (send_date, channel_provider, channel_id)
);

-- Backs the claim phase's own "every still-pending row for today" scan.
CREATE INDEX digest_send_state_pending_idx ON digest_send_state (send_date, status);
