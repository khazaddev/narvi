package sandbox_test

import (
	"strings"
	"testing"

	"github.com/narvidev/narvi/internal/domain/sandbox"
)

// TestEvaluateWarmDecision exercises EvaluateWarmDecision's scenarios,
// including Narvi's own Booting and Suspect states in the skip set (see
// warmdecision.go's own comment).
func TestEvaluateWarmDecision(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		state        sandbox.WarmState
		wantKind     sandbox.WarmActionKind
		wantContains string
	}{
		{
			name:         "skip when sandbox already connected",
			state:        sandbox.WarmState{HasActiveWebSocket: true, Status: sandbox.StateReady},
			wantKind:     sandbox.WarmActionSkip,
			wantContains: "already connected",
		},
		{
			name:         "skip when already spawning (in-memory)",
			state:        sandbox.WarmState{Status: sandbox.StatePending, IsSpawningInMemory: true},
			wantKind:     sandbox.WarmActionSkip,
			wantContains: "already spawning",
		},
		{
			name:         "skip when sandbox status is spawning",
			state:        sandbox.WarmState{Status: sandbox.StateSpawning},
			wantKind:     sandbox.WarmActionSkip,
			wantContains: "spawning",
		},
		{
			name:         "skip when sandbox status is connecting",
			state:        sandbox.WarmState{Status: sandbox.StateConnecting},
			wantKind:     sandbox.WarmActionSkip,
			wantContains: "connecting",
		},
		{
			name:         "skip when sandbox status is booting (Narvi's own third boot phase)",
			state:        sandbox.WarmState{Status: sandbox.StateBooting},
			wantKind:     sandbox.WarmActionSkip,
			wantContains: "booting",
		},
		{
			name:         "skip when sandbox status is suspect (Narvi's own addition, no TS equivalent)",
			state:        sandbox.WarmState{Status: sandbox.StateSuspect},
			wantKind:     sandbox.WarmActionSkip,
			wantContains: "suspect",
		},
		{
			name:         "skip when sandbox status is stale",
			state:        sandbox.WarmState{Status: sandbox.StateStale},
			wantKind:     sandbox.WarmActionSkip,
			wantContains: "stale",
		},
		{
			name:         "skip when sandbox status is stopped",
			state:        sandbox.WarmState{Status: sandbox.StateStopped},
			wantKind:     sandbox.WarmActionSkip,
			wantContains: "stopped",
		},
		{
			name:         "skip when sandbox status is failed",
			state:        sandbox.WarmState{Status: sandbox.StateFailed},
			wantKind:     sandbox.WarmActionSkip,
			wantContains: "failed",
		},
		{
			name:     "spawn when conditions pass",
			state:    sandbox.WarmState{Status: sandbox.StatePending},
			wantKind: sandbox.WarmActionSpawn,
		},
		{
			name:     "spawn when status is empty (no sandbox yet)",
			state:    sandbox.WarmState{Status: ""},
			wantKind: sandbox.WarmActionSpawn,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := sandbox.EvaluateWarmDecision(tc.state)
			if got.Kind != tc.wantKind {
				t.Fatalf("Kind = %s, want %s (full action: %+v)", got.Kind, tc.wantKind, got)
			}
			if tc.wantContains != "" && !strings.Contains(got.Reason, tc.wantContains) {
				t.Errorf("Reason = %q, want it to contain %q", got.Reason, tc.wantContains)
			}
		})
	}
}

// TestWarmActionKind_String and TestInactivityActionKind_String cover the
// Stringer implementations' out-of-range fallback branches.
func TestWarmActionKind_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		kind sandbox.WarmActionKind
		want string
	}{
		{sandbox.WarmActionSpawn, "spawn"},
		{sandbox.WarmActionSkip, "skip"},
		{sandbox.WarmActionKind(999), "WarmActionKind(999)"},
	}
	for _, tc := range tests {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("String() = %q, want %q", got, tc.want)
		}
	}
}

func TestInactivityActionKind_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		kind sandbox.InactivityActionKind
		want string
	}{
		{sandbox.InactivityActionTimeout, "timeout"},
		{sandbox.InactivityActionExtend, "extend"},
		{sandbox.InactivityActionSchedule, "schedule"},
		{sandbox.InactivityActionKind(999), "InactivityActionKind(999)"},
	}
	for _, tc := range tests {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("String() = %q, want %q", got, tc.want)
		}
	}
}
