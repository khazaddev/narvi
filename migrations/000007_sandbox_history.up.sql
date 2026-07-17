-- sandbox_history: append-only record of superseded sandbox generations
-- (§3.2 "history goes to sandbox_history") — deliberately no unique
-- constraint on session_id, since many rows accumulate over a session's
-- life. Reuses the sandbox_status enum defined in 000006; do not redefine.
CREATE TABLE sandbox_history (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id   UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    gen          INTEGER NOT NULL,
    status       sandbox_status NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL,
    archived_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX sandbox_history_session_id_gen_idx ON sandbox_history (session_id, gen);
