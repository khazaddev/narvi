// Package automation holds the automation → invocation → run(s) domain
// model (Step 51, "automations: engine", §3.5): "automation → invocation →
// run(s) (one run per target, fan-out ≤10). At-most-one failure strike per
// invocation via CAS (UPDATE ... WHERE failure_counted_at IS NULL).
// Auto-pause after 3 consecutive failed invocations. Recovery sweeps:
// orphaned starting runs >5 min, running >90 min."
//
// No I/O, no time.Now(), no randomness (§11) -- every input a decision
// function here needs (counts, states, "now") is supplied by the caller
// (internal/app/automation). This mirrors internal/domain/imagebuild's own
// split exactly (EvaluateBackoff/ImageBuildStreakThreshold there; the
// analogous EvaluateFailureStrike/AutoPauseThreshold here) -- in fact
// domain/imagebuild.ImageBuildStreakThreshold's own doc comment already
// names this package's own "3" explicitly, as the same established
// constant this codebase already uses twice (sandbox.
// CircuitBreakerThreshold, ImageBuildStreakThreshold) before this package
// existed for real.
//
// # Three machines, three transition tables
//
// Automation (automation.go): Active ⇄ Paused -- the auto-pause/resume
// lifecycle a maintainer/admin eventually toggles (mockups.html's own
// Automations view: "auto-paused chip + Resume").
//
// Invocation (invocation.go): Pending -> Succeeded | Failed -- closed
// exactly once, when every one of its fanned-out runs has reached a
// terminal state. Mirrors internal/domain/plan.Status's own shape (one
// non-terminal state, terminal states with no outgoing edge) more closely
// than turn's six-state machine, since an invocation has no interesting
// internal progress of its own -- only "still waiting on its runs" vs.
// "decided".
//
// Run (run.go): Starting -> Running -> Succeeded | Failed -- named to
// match §3.5's own sweep vocabulary exactly ("orphaned starting runs...
// running..."). DeriveRunStatus derives a run's own status from its linked
// session's turn history, the SAME "derive an aggregate status from a
// child summary slice" shape internal/domain/session.DeriveStatus already
// establishes for a session's own turns -- Starting corresponds to no
// turn having reached Processing yet (matches a run whose sandbox may
// still be cold-starting -- exactly the condition the "starting >5 min"
// sweep threshold exists to catch), Running to a turn now Processing.
//
// # Closing an invocation vs. recording its failure-strike consequence
//
// Closing an invocation (Pending -> Succeeded/Failed, via Transition,
// app/automation's own closeInvocation) and recording a FAILED invocation's
// failure-strike consequence against its own automation's consecutive-
// failure streak (applyFailureStrike) are two separate steps, each its own
// guarded operation: mirrors internal/app/sessionactor/progressnotify.go's
// own documented "two, independent, both reused rather than invented"
// double-guard precedent. The invocation's own status transition is guarded
// by its own current status (the Transition table's usual precondition,
// exactly like turn/plan/sandbox); the strike accounting is SEPARATELY
// guarded by automation_invocations.failure_counted_at IS NULL (§3.5's own
// literal CAS idiom, the same UPDATE ... WHERE <nullable-timestamp> IS NULL
// shape already established by TurnStore.MarkProgressNotified/
// ApprovePlanIfAwaitingApproval) -- so a crash between closeInvocation
// committing and a retried close attempt can never double-count the SAME
// invocation's failure against the streak twice, even though the
// invocation's own status is by then already terminal and would otherwise
// short-circuit a naive single-guard re-check.
//
// That failure_counted_at guard now runs INSIDE the SAME transaction as
// AutomationStore's own LockForUpdate/ApplyFailureStrike (app/automation's
// own closeout.go, applyFailureStrike) -- matching the "MUST run inside the
// same transaction" comments this package's own store/query layer has
// always carried (AutomationInvocationStore.WithTx, AutomationStore.WithTx,
// queries/automationinvocations.sql, queries/automations.sql). An earlier
// revision of this package's own writeup described the guard as committing
// STANDALONE, ahead of that transaction, as though that were a third,
// independently-committing step rather than a bug -- it was a bug: any
// failure between that standalone commit and the (separate) strike
// transaction's own commit (LockForUpdate, ApplyFailureStrike, or the
// commit itself) permanently set the one-way CAS guard with NO strike ever
// recorded, and nothing anywhere reads failure_counted_at to retry or
// reconcile it -- that invocation's own failure would silently and
// permanently never count toward auto-pause. Fusing the guard and its
// consequence into one atomic transaction closes that gap: either both the
// guard flip and the strike land together, or neither does, leaving a
// subsequent retry of applyFailureStrike free to win the still-unset guard
// and apply the strike for real.
//
// A crash between closeInvocation's OWN status-transition commit and this
// now-atomic strike transaction's own commit remains a SEPARATE, still-open
// residual gap -- closeInvocation's own Close call is not itself part of
// the same transaction as the strike accounting below it (see
// internal/app/automation/doc.go's own "closeInvocation's own Close call is
// not part of that same transaction" section, mirroring app/imagebuild/
// doc.go's own analogous claim-crash-gap precedent for why this is left as
// an accepted, documented gap rather than folded in here).
// This residual window is narrower than the one just closed above (Close
// and applyFailureStrike run back-to-back, milliseconds apart, versus the
// old standalone-guard gap, which could persist indefinitely with no retry
// path at all), but it is not zero.
//
// # EvaluateFailureStrike computes, the CAS-guarded store records
//
// EvaluateFailureStrike (strike.go) is a pure decision, mirroring
// domain/imagebuild.EvaluateBackoff/domain/sandbox.EvaluateCircuitBreaker's
// own shape exactly: given the CURRENT consecutive-failure count and
// whether the invocation that just closed failed, it returns the new count
// and whether that crosses AutoPauseThreshold (3). It does not itself
// count anything, or decide whether THIS particular failure has already
// been counted -- that is the CAS's job, entirely in app/automation's own
// impure layer, per this package's own "no I/O" boundary (§11).
package automation
