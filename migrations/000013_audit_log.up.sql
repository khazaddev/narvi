-- audit_log: literal shape from §13.3 ("audit_log(actor_user_id, action,
-- resource_type, resource_id, detail_json, correlation_id, created_at)").
--
-- actor_user_id is nullable here, diverging from §13.3's implied NOT NULL:
-- at this early schema-skeleton stage there is no seeded "system/bot" user
-- row to attribute system-initiated audit entries to yet. Revisit once a
-- system actor exists (identities/RBAC work, PR-39).
--
-- resource_id is TEXT, not UUID — resource_type varies polymorphically
-- across tables, so the id representation must too.
CREATE TABLE audit_log (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_user_id   UUID REFERENCES users(id) ON DELETE SET NULL,
    action          TEXT NOT NULL,
    resource_type   TEXT NOT NULL,
    resource_id     TEXT NOT NULL,
    detail_json     JSONB NOT NULL DEFAULT '{}'::jsonb,
    correlation_id  TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
