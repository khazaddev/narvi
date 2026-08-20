package turn

import "time"

// DeadlineConfig configures EvaluateTurnDeadline. Populated by the caller
// from platform.Timeouts.TurnDeadline (already defined at unused
// until now — Chain A: ProviderHardCap > SupervisorTurnCap > TurnDeadline
// > SSEInactivityTimeout, §5.4).
type DeadlineConfig struct {
	// Deadline is the maximum time a turn may spend in Processing before
	// it is timed out.
	Deadline time.Duration
}

// DeadlineResult is EvaluateTurnDeadline's verdict.
type DeadlineResult struct {
	// IsTimedOut reports whether the turn has exceeded its deadline.
	IsTimedOut bool
	// Elapsed is how long it's been since the turn entered Processing.
	Elapsed time.Duration
}

// EvaluateTurnDeadline evaluates the `turn_deadline` named persistent
// timer (§2, §3.3) for a turn at or past Dispatched.
//
// Measures from dispatchedAt — when the turn entered Dispatched (the
// Pending → Dispatched transition, TriggerDispatch, which per §3.3 only
// fires once a live sandbox has already been handed the turn: "dispatch
// happens when sandbox connects") — not from when it was first enqueued.
// See doc.go's design-call #1 for why this transition, not
// Dispatched → Processing, is the one that arms the timer: by the time
// Dispatched is reached, spawn/connect latency has already been fully
// absorbed by its own independent watchdog budget (§3.2's
// first_connect_budget), so nothing is double-counted — and arming here
// instead of at Processing means the hand-off gap between "sandbox
// connected" and "agent confirmed it started" is bounded too, instead of
// being invisible to every watchdog.
func EvaluateTurnDeadline(dispatchedAt, now time.Time, cfg DeadlineConfig) DeadlineResult {
	elapsed := now.Sub(dispatchedAt)
	return DeadlineResult{
		IsTimedOut: elapsed >= cfg.Deadline,
		Elapsed:    elapsed,
	}
}
