-- identities: links one user to a provider-side identity (§13.2 identity
-- graph). email is nullable — unknown until fetched from the provider API,
-- but §13.2's failure rule ("never null-out an email on transient failure")
-- means once set it must not be overwritten with null.
CREATE TYPE identity_provider AS ENUM ('github', 'slack', 'linear', 'google');
CREATE TYPE identity_linked_via AS ENUM ('auto_email', 'prompt', 'admin');

CREATE TABLE identities (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider       identity_provider NOT NULL,
    external_id    TEXT NOT NULL,
    email          TEXT,
    email_verified BOOLEAN NOT NULL DEFAULT false,
    linked_via     identity_linked_via NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider, external_id)
);
