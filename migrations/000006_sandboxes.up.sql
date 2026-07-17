-- sandboxes: the live sandbox for a session (§3.2, the critical machine).
-- UNIQUE(session_id) is the DB-level enforcement of "one sandbox row per
-- session"; gen is bumped by the app on every spawn/restore (monotonic,
-- per session); last_seen_at is nullable until the first liveness signal.
CREATE TYPE sandbox_status AS ENUM (
    'pending', 'spawning', 'connecting', 'booting', 'ready',
    'snapshotting', 'suspect', 'stopped', 'failed'
);

CREATE TABLE sandboxes (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id    UUID NOT NULL UNIQUE REFERENCES sessions(id) ON DELETE CASCADE,
    gen           INTEGER NOT NULL DEFAULT 1,
    status        sandbox_status NOT NULL DEFAULT 'pending',
    last_seen_at  TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
