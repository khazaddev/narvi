package sandbox_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/sandbox"
)

// TestReconcileTerminalSandboxStatus is table-driven over every state,
// proving transient boot statuses collapse to stopped, a live sandbox is
// left untouched, and an already-terminal or snapshotting sandbox is left
// untouched too.
func TestReconcileTerminalSandboxStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     sandbox.State
		wantTo     sandbox.State
		wantChange bool
	}{
		{"pending collapses to stopped", sandbox.StatePending, sandbox.StateStopped, true},
		{"spawning collapses to stopped", sandbox.StateSpawning, sandbox.StateStopped, true},
		{"connecting collapses to stopped", sandbox.StateConnecting, sandbox.StateStopped, true},
		{"booting collapses to stopped", sandbox.StateBooting, sandbox.StateStopped, true},

		{"ready is left untouched (live)", sandbox.StateReady, "", false},
		{"snapshotting is left untouched (live)", sandbox.StateSnapshotting, "", false},
		{"suspect is left untouched (live)", sandbox.StateSuspect, "", false},

		{"stopped is left untouched (already terminal)", sandbox.StateStopped, "", false},
		{"failed is left untouched (already terminal)", sandbox.StateFailed, "", false},
		{"stale is left untouched (already terminal)", sandbox.StateStale, "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			gotTo, gotChange := sandbox.ReconcileTerminalSandboxStatus(tc.status)
			if gotChange != tc.wantChange {
				t.Errorf("ReconcileTerminalSandboxStatus(%s) changed = %v, want %v", tc.status, gotChange, tc.wantChange)
			}
			if gotChange && gotTo != tc.wantTo {
				t.Errorf("ReconcileTerminalSandboxStatus(%s) = %s, want %s", tc.status, gotTo, tc.wantTo)
			}
		})
	}
}
