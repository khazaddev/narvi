package automation_test

import (
	"testing"

	"github.com/narvidev/narvi/internal/domain/automation"
)

func TestEvaluateFailureStrike(t *testing.T) {
	tests := []struct {
		name                       string
		currentConsecutiveFailures int
		invocationFailed           bool
		wantNewConsecutiveFailures int
		wantAutoPause              bool
	}{
		{"success resets from zero", 0, false, 0, false},
		{"success resets from a positive streak", 2, false, 0, false},
		{"first failure", 0, true, 1, false},
		{"second consecutive failure", 1, true, 2, false},
		{"third consecutive failure crosses threshold", 2, true, 3, true},
		{"fourth consecutive failure keeps reporting true", 3, true, 4, true},
		{"negative streak treated as zero", -5, true, 1, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := automation.EvaluateFailureStrike(tt.currentConsecutiveFailures, tt.invocationFailed)
			if got.NewConsecutiveFailures != tt.wantNewConsecutiveFailures {
				t.Errorf("NewConsecutiveFailures = %d, want %d", got.NewConsecutiveFailures, tt.wantNewConsecutiveFailures)
			}
			if got.ShouldAutoPause != tt.wantAutoPause {
				t.Errorf("ShouldAutoPause = %v, want %v", got.ShouldAutoPause, tt.wantAutoPause)
			}
		})
	}
}

// TestAutoPauseThresholdMatchesEstablishedConvention pins the "3" this
// package reuses (doc comment on AutoPauseThreshold) against a change that
// would silently drift it away from sandbox.CircuitBreakerThreshold/
// imagebuild.ImageBuildStreakThreshold's own identical value -- not a
// cross-package import (that would be a layering violation this codebase
// avoids between sibling domain packages with no other relationship), just
// a literal pin so a future edit here is a deliberate, visible diff.
func TestAutoPauseThresholdMatchesEstablishedConvention(t *testing.T) {
	if automation.AutoPauseThreshold != 3 {
		t.Fatalf("AutoPauseThreshold = %d, want 3 (see doc comment)", automation.AutoPauseThreshold)
	}
}
