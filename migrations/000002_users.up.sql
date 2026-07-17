-- users: one row per person, §13.2 identity graph anchor, §13.3 RBAC role.
CREATE TYPE user_role AS ENUM ('admin', 'maintainer', 'member', 'viewer');

CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    primary_email TEXT NOT NULL UNIQUE,
    display_name  TEXT NOT NULL,
    role          user_role NOT NULL DEFAULT 'member',
    disabled      BOOLEAN NOT NULL DEFAULT false,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
