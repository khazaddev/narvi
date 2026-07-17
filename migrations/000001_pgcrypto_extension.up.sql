-- pgcrypto provides gen_random_uuid(), used as the default for every UUID
-- primary key in this schema (§2, §13.2).
CREATE EXTENSION IF NOT EXISTS pgcrypto;
