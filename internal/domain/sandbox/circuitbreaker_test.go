package sandbox_test

import (
	"testing"
	"time"

	"github.com/narvidev/narvi/internal/domain/sandbox"
)

// TestEvaluateCircuitBreaker exercises EvaluateCircuitBreaker's scenarios
// and boundary conditions per §3.2.
func TestEvaluateCircuitBreaker(t *testing.T) {
	t.Parallel()

	cfg := sandbox.CircuitBreakerConfig{
		Threshold: 3,
		Window:    5 * time.Minute,
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name          string
		state         sandbox.CircuitBreakerState
		wantProceed   bool
		wantReset     bool
		wantWaitEqual time.Duration // only checked if wantProceed is false
	}{
		{
			name:        "allows spawn when no failures",
			state:       sandbox.CircuitBreakerState{FailureCount: 0, LastFailureTime: time.Time{}},
			wantProceed: true,
			wantReset:   false,
		},
		{
			name:        "allows spawn when failures below threshold",
			state:       sandbox.CircuitBreakerState{FailureCount: 2, LastFailureTime: now.Add(-1 * time.Minute)},
			wantProceed: true,
			wantReset:   false,
		},
		{
			name:          "blocks spawn after threshold failures within window",
			state:         sandbox.CircuitBreakerState{FailureCount: 3, LastFailureTime: now.Add(-1 * time.Minute)},
			wantProceed:   false,
			wantReset:     false,
			wantWaitEqual: cfg.Window - time.Minute,
		},
		{
			name:          "returns correct wait time when blocked",
			state:         sandbox.CircuitBreakerState{FailureCount: 5, LastFailureTime: now.Add(-2 * time.Minute)},
			wantProceed:   false,
			wantWaitEqual: cfg.Window - 2*time.Minute,
		},
		{
			name:        "signals reset when window passes",
			state:       sandbox.CircuitBreakerState{FailureCount: 5, LastFailureTime: now.Add(-cfg.Window - time.Second)},
			wantProceed: true,
			wantReset:   true,
		},
		{
			name:        "handles boundary timing (exact window): resets",
			state:       sandbox.CircuitBreakerState{FailureCount: 3, LastFailureTime: now.Add(-cfg.Window)},
			wantProceed: true,
			wantReset:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := sandbox.EvaluateCircuitBreaker(tc.state, cfg, now)
			if got.ShouldProceed != tc.wantProceed {
				t.Errorf("ShouldProceed = %v, want %v", got.ShouldProceed, tc.wantProceed)
			}
			if got.ShouldReset != tc.wantReset {
				t.Errorf("ShouldReset = %v, want %v", got.ShouldReset, tc.wantReset)
			}
			if !tc.wantProceed && got.WaitTime != tc.wantWaitEqual {
				t.Errorf("WaitTime = %v, want %v", got.WaitTime, tc.wantWaitEqual)
			}
			if tc.wantProceed && got.WaitTime != 0 {
				t.Errorf("WaitTime = %v, want 0 when ShouldProceed", got.WaitTime)
			}
		})
	}
}

// TestEvaluateCircuitBreaker_DefaultConfigWiring proves
// CircuitBreakerThreshold (the domain-level int constant) matches §3.2's
// explicit "3 permanent spawn failures".
func TestEvaluateCircuitBreaker_DefaultConfigWiring(t *testing.T) {
	t.Parallel()

	if sandbox.CircuitBreakerThreshold != 3 {
		t.Errorf("CircuitBreakerThreshold = %d, want 3 (§3.2, explicit)", sandbox.CircuitBreakerThreshold)
	}
}
