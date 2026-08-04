package automation

// AutoPauseThreshold is the number of CONSECUTIVE failed invocations that
// auto-pauses an automation (§3.5, explicit: "Auto-pause after 3
// consecutive failed invocations"). Mirrors sandbox.CircuitBreakerThreshold
// ("3 permanent spawn failures") and imagebuild.ImageBuildStreakThreshold's
// own identical "3" exactly -- imagebuild's own doc comment already names
// THIS package's own threshold explicitly as the reason it reused the same
// number rather than inventing a fourth: this codebase treats 3 consecutive
// failures as its own established "this is not a blip" signal, and this is
// the third place that number is used, not a fresh choice.
const AutoPauseThreshold = 3

// StrikeDecision is EvaluateFailureStrike's verdict.
type StrikeDecision struct {
	// NewConsecutiveFailures is the automation's own consecutive-failure
	// count after this invocation's outcome is applied -- 0 if the
	// invocation succeeded (a success resets the streak entirely, exactly
	// like sandbox.EvaluateCircuitBreaker's own ShouldReset semantics),
	// otherwise the prior count plus one.
	NewConsecutiveFailures int
	// ShouldAutoPause reports whether NewConsecutiveFailures has reached
	// AutoPauseThreshold -- true on every failed invocation from the
	// threshold onward (not just the first crossing), mirroring
	// imagebuild.BackoffDecision.StreakAlert's own identical "keep
	// reporting true, not just once" convention. The caller (app/
	// automation's own closeout.go) applies automation.TriggerAutoPause
	// only when this is true AND the automation is not already Paused
	// (Transition itself rejects a redundant Active-state-only edge from
	// an already-Paused automation, so a caller that calls Transition
	// unconditionally on every ShouldAutoPause-true outcome gets a safe,
	// typed no-op via IllegalTransitionError for an already-paused one,
	// not a silent double-pause).
	ShouldAutoPause bool
}

// EvaluateFailureStrike computes an automation's new consecutive-failure
// count and whether it should auto-pause, given currentConsecutiveFailures
// (the automation's OWN persisted count, before this invocation's outcome)
// and invocationFailed (this invocation's own EvaluateInvocationOutcome.
// Failed). currentConsecutiveFailures < 0 is treated as 0 (defensive: there
// is no such thing as a negative streak).
//
// This function does not itself decide whether THIS invocation's failure
// has already been counted once before -- that is the CAS's job (§3.5:
// "At-most-one failure strike per invocation via CAS"), entirely in app/
// automation's own impure layer (automation_invocations.failure_counted_at
// IS NULL, see this package's own doc.go). EvaluateFailureStrike is called
// AT MOST once per invocation, by construction of that CAS guard -- this
// function has no way to enforce that itself, matching domain/imagebuild.
// EvaluateBackoff's own identical "caller already increments attemptCount
// exactly once per real attempt" precedent.
func EvaluateFailureStrike(currentConsecutiveFailures int, invocationFailed bool) StrikeDecision {
	if currentConsecutiveFailures < 0 {
		currentConsecutiveFailures = 0
	}

	if !invocationFailed {
		return StrikeDecision{NewConsecutiveFailures: 0, ShouldAutoPause: false}
	}

	next := currentConsecutiveFailures + 1
	return StrikeDecision{
		NewConsecutiveFailures: next,
		ShouldAutoPause:        next >= AutoPauseThreshold,
	}
}
