-- events: append-only per-session event log, cursor-paginated per §6.2
-- fetch_history. A monotonic BIGSERIAL id (not a UUID) is the natural
-- pagination cursor. type is kept TEXT (not an enum) — §6.1 lists a long,
-- evolving set of event type names and later PRs add more without an
-- ALTER TYPE migration.
CREATE TABLE events (
    id          BIGSERIAL PRIMARY KEY,
    session_id  UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    type        TEXT NOT NULL,
    payload     JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX events_session_id_id_idx ON events (session_id, id);
