-- turns: prompt lifecycle within a session (§3.3). conversation_id is
-- nullable — recorded at turn start, so absent before then. The partial
-- unique index enforces "exactly one processing per session" at the DB
-- level, not just in the domain machine.
CREATE TYPE turn_status AS ENUM ('pending', 'dispatched', 'processing', 'completed', 'failed', 'cancelled');

CREATE TABLE turns (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id      UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    status          turn_status NOT NULL DEFAULT 'pending',
    conversation_id TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    dispatched_at   TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ
);

CREATE UNIQUE INDEX turns_one_processing_per_session ON turns (session_id) WHERE status = 'processing';
