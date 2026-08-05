-- automations: the top of Step 51's own ("automations: engine", §3.5)
-- automation -> invocation -> run(s) domain model
-- (internal/domain/automation). This table is deliberately minimal: Step
-- 52 ("automations: triggers & extras", §8.4) owns trigger conditions
-- (GitHub/Linear/webhook/cron), sandboxSettings, and per-automation
-- secrets, and will ALTER this table to add them rather than this Step
-- inventing placeholder columns for a shape it does not yet specify --
-- see internal/domain/automation/doc.go for the full domain writeup and
-- this Step's own explicit scope boundary against Step 52.
--
-- repos is the SAME shape as sessions.repos (migrations/000004_sessions.
-- up.sql) -- a JSONB array of {name, url, branch} objects, never a scalar
-- single-repo mirror (this repo's own CLAUDE.md: "repos are always a
-- list") -- capped at internal/domain/automation.MaxFanOutTargets (10) by
-- application code, not a CHECK constraint (mirrors this codebase's own
-- established precedent of validating JSONB-array cardinality/shape in Go
-- before it is ever persisted, e.g. httpapi.validateCreateSessionRequest's
-- own repos validation, rather than duplicating that logic as SQL).
--
-- consecutive_failures/status back internal/domain/automation's own
-- EvaluateFailureStrike/Transition (§3.5: "at-most-one failure strike per
-- invocation via CAS... auto-pause after 3 consecutive failed
-- invocations") -- consecutive_failures is reset to 0 by a succeeded
-- invocation's own close-out and incremented by a failed one, both via
-- internal/app/automation's own closeout step; status flips to 'paused'
-- exactly when that increment crosses automation.AutoPauseThreshold.
--
-- created_by is nullable (ON DELETE SET NULL, mirrors sessions.created_by's
-- own identical nullability/cascade choice) -- an automation, like a
-- session, can outlive the user who created it.
CREATE TYPE automation_status AS ENUM ('active', 'paused');

CREATE TABLE automations (
    id     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name   TEXT NOT NULL,
    prompt TEXT,
    repos  JSONB NOT NULL,

    status               automation_status NOT NULL DEFAULT 'active',
    consecutive_failures INTEGER NOT NULL DEFAULT 0,

    created_by UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
