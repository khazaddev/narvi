-- artifacts: PR / preview / uploads surfaced in the session workspace
-- (§12.2 item 1).
CREATE TYPE artifact_type AS ENUM ('pr', 'preview', 'upload');

CREATE TABLE artifacts (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id  UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    type        artifact_type NOT NULL,
    url         TEXT NOT NULL,
    metadata    JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
