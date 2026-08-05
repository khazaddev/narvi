-- automation_invocations: one firing of an automation (§3.5: "automation
-- -> invocation -> run(s)"). Step 52 owns WHAT causes one of these to be
-- created (GitHub/Linear/webhook/cron trigger evaluation, §8.4) -- this
-- Step owns everything that happens once one exists: fanning it out into
-- ≤10 automation_runs rows (one per target) and deciding its own overall
-- outcome once every run it fanned out has reached a terminal state.
--
-- targets is a snapshot of the automation's own repos JSONB (migrations/
-- 000051_automations.up.sql) taken AT INVOCATION-CREATION TIME, not a live
-- reference re-read from the automation row later -- an automation's own
-- repos could change between invocation creation and fan-out/close-out,
-- and this invocation's own runs must stay a fixed record of what it
-- ACTUALLY targeted (mirrors sessions.repos' own identical "a snapshot,
-- never re-derived" precedent). total_runs = len(targets), denormalized
-- so the close-out step's own "every run terminal yet" check
-- (queries/automationinvocations.sql) is a single indexed count comparison
-- against automation_runs, never a second read of targets' own JSONB
-- length.
--
-- Two INDEPENDENT nullable-timestamp CAS guards (internal/domain/
-- automation/doc.go's own "two independent CAS guards, not one" section
-- explains why these are kept separate rather than folded into `status`):
--
--   - fanned_out_at: NULL until internal/app/automation.Engine's own main
--     pump claims this row (a genuine `UPDATE ... WHERE fanned_out_at IS
--     NULL` CAS, the SAME idiom TurnStore.MarkProgressNotified/
--     ApprovePlanIfAwaitingApproval already establish) and begins creating
--     this invocation's own runs+sessions -- guards against a crash-and-
--     retry double-fanning-out the SAME invocation into 20 runs instead of
--     10.
--   - failure_counted_at: NULL until the failure-strike consequence of a
--     FAILED invocation has been applied against its own automation's
--     consecutive_failures counter (§3.5: "at-most-one failure strike per
--     invocation via CAS... UPDATE ... WHERE failure_counted_at IS NULL"
--     -- this column IS that literal column). Left NULL forever for an
--     invocation that succeeds -- a success never counts a strike, so
--     there is nothing to guard there.
--
-- status/closed_at are the OUTCOME half (internal/domain/automation's own
-- InvocationTransition, Pending -> Succeeded|Failed) -- deliberately a
-- SEPARATE concept from either CAS guard above: status answers "what
-- happened", the two timestamp columns answer "have we already reacted to
-- it exactly once" for their own respective, independent reaction.
CREATE TYPE automation_invocation_status AS ENUM ('pending', 'succeeded', 'failed');

CREATE TABLE automation_invocations (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    automation_id UUID NOT NULL REFERENCES automations(id) ON DELETE CASCADE,

    status     automation_invocation_status NOT NULL DEFAULT 'pending',
    targets    JSONB NOT NULL,
    total_runs INTEGER NOT NULL,

    fanned_out_at      TIMESTAMPTZ,
    failure_counted_at TIMESTAMPTZ,
    closed_at          TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Backs the fan-out pump's own claim query (ListDueForFanOut,
-- queries/automationinvocations.sql): every not-yet-fanned-out row,
-- oldest first.
CREATE INDEX automation_invocations_pending_fanout_idx ON automation_invocations (created_at) WHERE fanned_out_at IS NULL;

-- Backs per-automation history/health lookups (this Step's own
-- reconcile/closeout queries, and the future Step 76 read model the
-- mockups' own "12/12 ok" / "n/3 strikes" health column will need) and
-- gives the automations(id) ON DELETE CASCADE an index to delete against
-- instead of a full table scan.
CREATE INDEX automation_invocations_automation_id_idx ON automation_invocations (automation_id);
