package sandbox

import "time"

// CircuitBreakerThreshold is the number of failures that opens the circuit
// (§3.2, explicit: "3 permanent spawn failures within 5 min blocks
// spawning"). A plain int, not a duration -- it does not belong in
// platform/timeouts.go (that file is durations only). The companion window
// (5min) IS a duration and lives there as platform.Timeouts.
// CircuitBreakerWindow.
const CircuitBreakerThreshold = 3

// CircuitBreakerState is the circuit breaker's persisted state.
type CircuitBreakerState struct {
	// FailureCount is the number of consecutive spawn failures counted
	// against the breaker.
	//
	// CONTRACT for the future caller (Step 12's SandboxProvider /
	// ProviderError -- this package does not itself see provider errors):
	// only PERMANENT provider failures may increment this counter. §3.2:
	// "Unknown provider errors default to transient, never permanent -- a
	// novel transient failure must not trip the breaker." This package
	// does not enforce that classification; it is the caller's, entirely.
	FailureCount int
	// LastFailureTime is when the most recent counted failure occurred.
	// Zero (time.Time{}) means no failure has ever been recorded.
	LastFailureTime time.Time
}

// CircuitBreakerConfig configures the breaker.
type CircuitBreakerConfig struct {
	// Threshold is normally CircuitBreakerThreshold; kept as a field
	// (rather than hardcoding the constant into EvaluateCircuitBreaker) so
	// the function stays a pure, fully parameterized decision like every
	// other one in this package.
	Threshold int
	// Window is the sliding window failures are counted within. Populated
	// by the caller from platform.Timeouts.CircuitBreakerWindow.
	Window time.Duration
}

// CircuitBreakerDecision is the breaker's verdict.
type CircuitBreakerDecision struct {
	// ShouldProceed reports whether spawning may proceed.
	ShouldProceed bool
	// ShouldReset reports whether the failure count should be reset
	// (the window has passed since the last counted failure).
	ShouldReset bool
	// WaitTime is how long the caller must wait before the circuit
	// closes. Zero unless ShouldProceed is false.
	WaitTime time.Duration
}

// EvaluateCircuitBreaker decides whether the sandbox spawn circuit breaker
// allows spawning right now.
func EvaluateCircuitBreaker(state CircuitBreakerState, cfg CircuitBreakerConfig, now time.Time) CircuitBreakerDecision {
	timeSinceLastFailure := now.Sub(state.LastFailureTime)

	// The window has passed since the last counted failure: reset.
	if state.FailureCount > 0 && timeSinceLastFailure >= cfg.Window {
		return CircuitBreakerDecision{ShouldProceed: true, ShouldReset: true}
	}

	// Too many failures within the window: open (block spawning).
	if state.FailureCount >= cfg.Threshold && timeSinceLastFailure < cfg.Window {
		return CircuitBreakerDecision{
			ShouldProceed: false,
			ShouldReset:   false,
			WaitTime:      cfg.Window - timeSinceLastFailure,
		}
	}

	// Closed: spawning allowed.
	return CircuitBreakerDecision{ShouldProceed: true, ShouldReset: false}
}
