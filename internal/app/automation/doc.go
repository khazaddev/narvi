// Package automation is the process-wide background automation engine
// (Step 51, "automations: engine", §3.5) -- a sibling of app/reconciler,
// app/imagebuild, and app/outboxworker, not folded into any of them
// (TECHNICAL_PLAN.md §1's own repo-layout convention: one package per
// major loop/subsystem under internal/app/). See internal/domain/
// automation for the pure decision functions this package's own impure
// loops are built around.
//
// Engine.Run (mirroring app/imagebuild.Builder.Run's own "fan out more
// than one independent ticker loop via a zero-value errgroup.Group" shape
// exactly) fans out THREE independent ticker loops:
//
//  1. runFanOutPump (platform.Timeouts.AutomationEnginePumpInterval),
//     calling PumpOnce -- fanout.go: claims a batch of automation_invocations
//     rows not yet fanned out (ONE transaction: SELECT ... FOR UPDATE SKIP
//     LOCKED, then a per-row UPDATE ... WHERE fanned_out_at IS NULL,
//     committed BEFORE any real session-creation work -- mirrors
//     app/imagebuild.Builder.claimBatch's own claim-batch precedent
//     exactly), then for each claimed invocation, OUTSIDE any transaction,
//     fans out one automation_runs row PER target (§3.5: "one run per
//     target, fan-out ≤10" -- internal/domain/automation.MaxFanOutTargets
//     already bounds this at invocation-creation time, CreateInvocation
//     below). Each target's own run+session pair is created together, on
//     ONE freshly opened transaction, via the SAME httpapi.CreateSessionOnTx
//     core Step 31 established and Steps 32/33/34 already reuse three
//     times -- exactly mirroring internal/adapters/inbound/github's own
//     SessionCoalescer.CreateOrJoin, which calls CreateSessionOnTx inline on
//     its own already-open tx rather than the pool-based CreateSessionCore
//     (fanout.go's own createRunAndSession does the identical thing, for the
//     identical reason: the run row and its session must land in the SAME
//     transaction, so a crash between the two can never leave a run with no
//     session or a session with no corresponding run). One target's own
//     failure (repo validation, a Postgres error) is isolated -- logged,
//     recorded as a RunStatusFailed run, and does NOT abort the rest of that
//     invocation's own fan-out, exactly like app/imagebuild.Builder.attempt's
//     own per-row isolation.
//
//  2. runReconcilePump (platform.Timeouts.AutomationEnginePumpInterval),
//     calling ReconcileOnce -- reconcile.go: lists a bounded batch of
//     still-non-terminal automation_runs rows (a plain, unlocked SELECT --
//     see ListInFlightRuns' own generated doc comment for why no FOR UPDATE
//     SKIP LOCKED is needed here), and for each one whose own linked
//     session's turn history (automation.DeriveRunStatus) reports a NEW
//     status, applies the CAS-guarded write (PromoteToRunning or
//     Terminalize) and, for a terminalizing write this call actually won,
//     cascades to closeout.go's own maybeCloseInvocation.
//
//  3. runSweepPump (platform.Timeouts.AutomationSweepInterval), calling
//     SweepOnce -- sweep.go: §3.5's own two recovery-sweep thresholds
//     ("orphaned starting runs >5 min, running >90 min") -- a run whose own
//     started_at/running_at predates its own threshold
//     (automation.IsOrphaned, injected `now`) is terminalized via
//     RunTriggerOrphanTimeout and cascades through the SAME closeout.go path
//     a genuine turn-outcome terminalization does. Mirrors app/reconciler's
//     own periodic-sweep shape, with one deliberate simplification: unlike
//     app/reconciler.Reconciler's own in-memory, cross-tick "seen
//     unexplained since" debounce map (needed there because a live provider
//     ref's own absence from Postgres is genuinely ambiguous on first
//     sighting), a run's own started_at/running_at is already a durable
//     timestamp -- "has this run been stuck past its own threshold" is
//     answered by a single comparison against an injected `now`, with no
//     cross-tick memory required.
//
// # Two independent CAS guards, cascaded through one shared closeout path
//
// closeout.go's own maybeCloseInvocation/applyStrikeAccounting are the ONE
// place every terminalizing write (a genuine turn outcome, via
// ReconcileOnce, OR an orphan timeout, via SweepOnce) cascades through --
// never duplicated between the two callers. See internal/domain/
// automation/doc.go's own "two independent CAS guards, not one" section for
// why closing an invocation (automation_invocations.status, guarded by its
// own current status) and recording that closure's failure-strike
// consequence (automation_invocations.failure_counted_at IS NULL, §3.5's
// own literal CAS idiom) are two separate guarded steps, not one.
//
// # CreateInvocation -- this Step's own minimal entry point
//
// Step 52 ("automations: triggers & extras", §8.4) owns WHAT causes an
// invocation to be created (GitHub/Linear/webhook/cron trigger condition
// evaluation) -- out of this Step's own scope entirely. invocationenqueue.go's
// CreateInvocation is this Step's own minimal, durable "an invocation now
// exists, fan it out" hand-off (mirrors internal/app/releasereview.Enqueue's
// own "one cheap INSERT, the real work happens later on a dedicated
// background loop's own schedule" shape) -- callable directly by this
// package's own tests today, and ready for Step 52's own trigger evaluator to
// call unchanged once it exists. It does NOT itself decide whether an
// automation should fire; it only validates targets (automation.
// ValidateTargets) and durably records that a firing has already been
// decided.
package automation
