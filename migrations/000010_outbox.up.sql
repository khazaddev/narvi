-- outbox: standard outbox pattern (§5.1) for every outbound side effect.
-- session_id is nullable — not every outbound side effect is session-
-- scoped. kind is kept TEXT — the set of notifier kinds grows with the
-- PR-32/33/34/35 ingress work.
CREATE TYPE outbox_status AS ENUM ('pending', 'delivered', 'dead_letter');

CREATE TABLE outbox (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id       UUID REFERENCES sessions(id) ON DELETE CASCADE,
    kind             TEXT NOT NULL,
    payload          JSONB NOT NULL,
    status           outbox_status NOT NULL DEFAULT 'pending',
    attempts         INTEGER NOT NULL DEFAULT 0,
    next_attempt_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at     TIMESTAMPTZ,
    last_error       TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
