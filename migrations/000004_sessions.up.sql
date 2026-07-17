-- sessions: the top-level unit of work (§3.1). title is populated later by
-- the session_title WS event (§6.1); failure_reason is only set on a
-- terminal non-completed path; created_by is nullable because bot/
-- automation-created sessions may have no direct human user.
CREATE TYPE session_status AS ENUM ('created', 'active', 'completed', 'failed', 'cancelled');
CREATE TYPE session_failure_reason AS ENUM ('cancelled', 'failed', 'timeout', 'never_started');
CREATE TYPE session_spawn_source AS ENUM ('web', 'slack', 'linear', 'github');

CREATE TABLE sessions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title           TEXT,
    status          session_status NOT NULL DEFAULT 'created',
    failure_reason  session_failure_reason,
    archived        BOOLEAN NOT NULL DEFAULT false,
    spawn_source    session_spawn_source NOT NULL,
    created_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
