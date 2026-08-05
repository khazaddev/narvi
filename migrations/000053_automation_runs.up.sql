-- automation_runs: one run per target, fan-out ≤10 (§3.5). A run's own
-- lifecycle (internal/domain/automation's RunTransition: starting ->
-- running -> succeeded|failed) is DERIVED from its linked session's own
-- turn history (automation.DeriveRunStatus) by internal/app/automation's
-- own reconcile pump, never written directly by anything else -- exactly
-- like this codebase's own established "status is the output of a
-- Transition/derivation, not a field application code sets directly"
-- convention (internal/domain/session.DeriveStatus's own doc comment).
--
-- automation_id is denormalized from automation_invocations.automation_id
-- (itself already indexed) purely so a per-automation health rollup
-- (Step 76's own future read model; this Step's own reconcile/sweep
-- queries do not strictly need it, since they always join through
-- invocation_id) never has to join through automation_invocations at all
-- -- mirrors this codebase's own established "denormalize one hop when a
-- read model will otherwise always need to join it back" judgment (e.g.
-- outbox.session_id alongside outbox.correlation_id, both derivable via a
-- join, kept directly on the row anyway).
--
-- session_id is nullable (ON DELETE SET NULL, mirrors release_manifest_
-- pending's own session_id... except THAT column is NOT NULL+CASCADE,
-- since a release-manifest-pending row is meaningless without its
-- session; an automation_run is different: it is created and MUST
-- persist as a 'failed' record even if session/turn creation for its own
-- target fails before any session ever exists at all -- session_id starts
-- NULL and is set once CreateSessionOnTx succeeds for this run's target,
-- see internal/app/automation's own fanout.go) and SET NULL on delete
-- (a run is a durable outcome record in its own right -- deleting the
-- session it once pointed to must not cascade-delete the run alongside
-- it, unlike automation_invocations.automation_id/automation_runs.
-- invocation_id above, where the PARENT genuinely owns the child's whole
-- reason to exist).
--
-- started_at/running_at back §3.5's own two recovery-sweep thresholds
-- (platform.Timeouts.AutomationRunStartingOrphanThreshold/
-- AutomationRunRunningOrphanThreshold, automation.IsOrphaned) --
-- started_at is stamped once, at row creation (never updated again);
-- running_at is stamped once, the moment RunTriggerProcessing promotes a
-- run out of 'starting' -- each is the "since" instant its own threshold
-- measures from, never re-derived from created_at at sweep time (a run
-- could plausibly sit non-'running' for a while before its own turn
-- reaches Processing, and the running-orphan clock must start counting
-- from THAT moment, not from when the run row was first created).
CREATE TYPE automation_run_status AS ENUM ('starting', 'running', 'succeeded', 'failed');

CREATE TABLE automation_runs (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invocation_id UUID NOT NULL REFERENCES automation_invocations(id) ON DELETE CASCADE,
    automation_id UUID NOT NULL REFERENCES automations(id) ON DELETE CASCADE,

    target     JSONB NOT NULL,
    session_id UUID REFERENCES sessions(id) ON DELETE SET NULL,
    status     automation_run_status NOT NULL DEFAULT 'starting',

    started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    running_at   TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Backs the close-out step's own per-invocation aggregate count
-- (CountTerminalRunsForInvocation, queries/automationruns.sql) -- "how
-- many of this invocation's own runs are terminal, and how many of those
-- failed".
CREATE INDEX automation_runs_invocation_idx ON automation_runs (invocation_id);

-- Backs the reconcile pump's own in-flight batch claim (ListInFlight):
-- every run still starting/running, with a session to check, oldest
-- first.
CREATE INDEX automation_runs_inflight_idx ON automation_runs (created_at) WHERE status IN ('starting', 'running');

-- Backs the two recovery sweeps (ListOrphanedStarting/ListOrphanedRunning)
-- -- each partial index scoped to exactly the one status/timestamp column
-- its own sweep threshold compares against, so neither sweep's own query
-- ever scans a row in the other status (or a terminal one).
CREATE INDEX automation_runs_starting_sweep_idx ON automation_runs (started_at) WHERE status = 'starting';
CREATE INDEX automation_runs_running_sweep_idx ON automation_runs (running_at) WHERE status = 'running';
