-- environments: the persisted half of the Environment record §14.1
-- describes (internal/domain/environment.Environment already implements
-- its pure validation/derivation logic; this migration gives it somewhere
-- to live). Deliberately scoped to exactly the fields the domain struct
-- already has -- path_scope and mock_configured -- nothing more.
--
-- Scope decision (see this batch's own report): §14.1 describes
-- Environment, in its FULL eventual form, as a separately-managed, reusable
-- entity "referenced from session creation and automation targets"
-- (§3.5) -- but no REST endpoint for creating/managing an Environment on
-- its own exists anywhere in this codebase yet, and building one is out
-- of scope here. So, for now, an environments row is created INLINE, at
-- session-creation time only (httpapi.CreateSession), whenever a caller
-- supplies a non-empty pathScope -- never pre-created/reused by id. This
-- table's own shape does not preclude a later Step from adding real
-- CRUD + reuse-by-id on top of it.
CREATE TABLE environments (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    path_scope      JSONB,
    mock_configured BOOLEAN NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- sessions.environment_id / sessions.provenance_tag: both nullable, both
-- NULL on every existing/ordinary session -- exactly today's unscoped
-- behavior, unchanged. environment_id is set only when CreateSession
-- receives a non-empty pathScope (in which case a fresh environments row
-- is inserted in the same transaction as the session itself, and
-- environment_id points at it). provenance_tag is set from
-- internal/domain/environment.RequiresProvenanceTag's own real return
-- value at that same moment (§14.1: "Sessions created under a scoped
-- Environment carry a provenance tag ... so the label automation and the
-- handoff sentinel (§14.4) can act on it without re-deriving intent").
ALTER TABLE sessions ADD COLUMN environment_id UUID REFERENCES environments(id);
ALTER TABLE sessions ADD COLUMN provenance_tag TEXT;
