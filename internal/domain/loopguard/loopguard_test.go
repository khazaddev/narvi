package loopguard_test

import (
	"testing"

	"github.com/narvidev/narvi/internal/domain/loopguard"
)

func TestDefaultMaxAttempts_MatchesEstablishedThreshold(t *testing.T) {
	t.Parallel()

	// The codebase-wide "3 strikes" constant (sandbox.
	// CircuitBreakerThreshold, automation.AutoPauseThreshold,
	// imagebuild.ImageBuildStreakThreshold) -- pinned so a drive-by
	// change here is a visible, deliberate decision.
	if loopguard.DefaultMaxAttempts != 3 {
		t.Fatalf("DefaultMaxAttempts = %d, want 3", loopguard.DefaultMaxAttempts)
	}
}

func TestEvaluate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		attemptCount int
		maxAttempts  int
		wantProceed  bool
	}{
		{"no attempts yet under default bound", 0, 3, true},
		{"one attempt under bound", 1, 3, true},
		{"last permitted attempt (2 of 3 used)", 2, 3, true},
		{"bound exactly consumed", 3, 3, false},
		{"bound exceeded", 4, 3, false},
		{"far past the bound stays escalated (monotonic)", 100, 3, false},
		{"single-attempt bound: fresh loop proceeds", 0, 1, true},
		{"single-attempt bound: one attempt exhausts it", 1, 1, false},
		{"zero max attempts escalates immediately (fail-conservative misconfig)", 0, 0, false},
		{"negative max attempts escalates immediately", 0, -1, false},
		{"negative attempt count reads as no attempts", -1, 3, true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := loopguard.Evaluate(
				loopguard.State{AttemptCount: tc.attemptCount},
				loopguard.Config{MaxAttempts: tc.maxAttempts},
			)

			if got.ShouldProceed != tc.wantProceed {
				t.Errorf("Evaluate(%d attempts, max %d).ShouldProceed = %v, want %v",
					tc.attemptCount, tc.maxAttempts, got.ShouldProceed, tc.wantProceed)
			}
			if got.ShouldEscalate != !tc.wantProceed {
				t.Errorf("Evaluate(%d attempts, max %d).ShouldEscalate = %v, want %v",
					tc.attemptCount, tc.maxAttempts, got.ShouldEscalate, !tc.wantProceed)
			}
			if got.ShouldProceed == got.ShouldEscalate {
				t.Errorf("Evaluate(%d attempts, max %d) = %+v -- exactly one field must be true",
					tc.attemptCount, tc.maxAttempts, got)
			}
		})
	}
}

// TestEvaluate_MonotonicInAttemptCount proves the §25.5 property the
// package exists for: once the verdict flips to escalate it NEVER flips
// back as the count grows -- and with no time input in the signature at
// all, there is structurally no delay-based reset axis to regress on.
func TestEvaluate_MonotonicInAttemptCount(t *testing.T) {
	t.Parallel()

	cfg := loopguard.Config{MaxAttempts: loopguard.DefaultMaxAttempts}
	escalated := false
	for attempts := 0; attempts <= 10; attempts++ {
		d := loopguard.Evaluate(loopguard.State{AttemptCount: attempts}, cfg)
		if escalated && d.ShouldProceed {
			t.Fatalf("Evaluate flipped back to proceed at %d attempts after escalating earlier -- the bound must be monotonic", attempts)
		}
		if d.ShouldEscalate {
			escalated = true
		}
	}
	if !escalated {
		t.Fatal("Evaluate never escalated within 10 attempts at the default bound")
	}
}
