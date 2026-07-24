-- Step 37 ("plan mode, web", §8.1/§12.2 item 3/§13.3/§16.1) additions.
--
-- 1) sessions.build_model_id: the model used for the eventual
-- approval-dispatched IMPLEMENTATION turn -- distinct from turns.model_id
-- (which already carries the PLAN turn's own model, per migration
-- 000018_session_repos.up.sql). Null means "use the default model catalog
-- entry", the exact same convention turns.model_id/CreateSessionRequest.
-- modelId already establish (§12.2 item 3's own mockup: "plan: opus ·
-- build: sonnet", header-visible). Session-scoped, not turn-scoped,
-- because it is a set-once value for the session's own eventual build
-- turn(s), not resubmitted on every request-changes turn (see restdtos.
-- CreateSessionRequest.buildModelId's own doc comment,
-- contracts/rest/v1/dtos.schema.json).
ALTER TABLE sessions ADD COLUMN build_model_id TEXT;

-- 2) plan_status: the plan document's own approval-state taxonomy (§16.1's
-- "awaiting_approval" item kind, plus the terminal outcomes). Deliberately
-- a BRAND NEW, independent enum/table -- NOT a new value added to
-- session_status or turn_status (both are closed, exhaustive machines
-- with no default/wildcard case, internal/domain/session/turn) -- exactly
-- the same pattern Step 36's intent_decision/prompt_templates and Steps
-- 33/34's slack_thread_sessions/linear_agent_sessions already establish:
-- a plan's own lifecycle is tracked by a table of its own, alongside the
-- turn/session machines, never inside them.
CREATE TYPE plan_status AS ENUM ('awaiting_approval', 'approved', 'rejected', 'superseded');

-- 3) plans: one row per plan VERSION (§12.2 item 3: "persistent versioned
-- plan document ... v1->v2 history"). turn_id names the turn that
-- PRODUCED this version's content (the plan_mode=true turn whose
-- completion this row records) -- the plan's own text/steps live in that
-- turn's already-existing event stream (assistant text events), not
-- duplicated into this table; this table is the approval/versioning
-- ledger, not a second copy of the plan document's own prose.
--
-- plan_model_id is copied from the producing turn's own model_id AT
-- CREATION TIME (not re-read from turns.model_id later), so a plan's own
-- historical record of which model actually produced it survives even if
-- the session's own turns.model_id convention or default model catalog
-- entry later changes -- §12.2 item 3's own "plan-model vs build-model
-- split visible in header" needs a stable per-version answer, not a
-- live-recomputed one.
--
-- The partial unique index below is what makes "at most one
-- awaiting_approval plan per session" a real DB-level guarantee, not just
-- an application convention (§12.2 item 3 "approval targets a specific
-- version"; §16.1's "first verdict wins" cross-channel guarantee Step 38
-- builds on is only sound because of this) -- a "request changes" turn's
-- own plan-row-creation logic (internal/app/sessionactor, hooked into the
-- SAME transaction the turn's own terminal-state write already uses) must
-- mark the prior awaiting_approval row 'superseded' BEFORE/ATOMICALLY-WITH
-- inserting the new one, in the same transaction, or this index rejects
-- the insert.
CREATE TABLE plans (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id    UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    turn_id       UUID NOT NULL REFERENCES turns(id),
    version       INTEGER NOT NULL,
    status        plan_status NOT NULL,
    plan_model_id TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at    TIMESTAMPTZ,
    decided_by    UUID REFERENCES users(id) ON DELETE SET NULL
);

CREATE UNIQUE INDEX plans_one_awaiting_approval_per_session
    ON plans (session_id)
    WHERE status = 'awaiting_approval';
