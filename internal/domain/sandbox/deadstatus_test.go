package sandbox_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/sandbox"
)

// TestIsDeadSandboxStatus is table-driven over every known state, proving
// the deny-list is exactly {stopped, stale, failed} and every other known
// state -- including an unrecognized future one -- reads as live.
func TestIsDeadSandboxStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status sandbox.State
		want   bool
	}{
		{sandbox.StatePending, false},
		{sandbox.StateSpawning, false},
		{sandbox.StateConnecting, false},
		{sandbox.StateBooting, false},
		{sandbox.StateReady, false},
		{sandbox.StateSnapshotting, false},
		{sandbox.StateSuspect, false},
		{sandbox.StateStopped, true},
		{sandbox.StateFailed, true},
		{sandbox.StateStale, true},
		{sandbox.State("some_future_status"), false}, // deny-list, not allow-list
	}

	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			t.Parallel()
			if got := sandbox.IsDeadSandboxStatus(tc.status); got != tc.want {
				t.Errorf("IsDeadSandboxStatus(%s) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}
