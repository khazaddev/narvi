-- session_timers: named persistent timers (§2 "session_timers(session_id,
-- name, fires_at)"). name is kept TEXT, not an enum — §2's name list is
-- documentation of examples, not a closed set. UNIQUE(session_id, name)
-- means re-arming a named timer is an upsert, never a duplicate insert.
CREATE TABLE session_timers (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id  UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    fires_at    TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (session_id, name)
);
