package sandbox_test

import (
	"testing"
	"time"

	"github.com/narvidev/narvi/internal/domain/sandbox"
)

// TestEvaluateInactivityTimeout exercises EvaluateInactivityTimeout's
// scenarios. Narvi has a single live sandbox state (Ready) -- IsProcessing
// carries the "a turn is in flight" signal instead of a distinct status.
func TestEvaluateInactivityTimeout(t *testing.T) {
	t.Parallel()

	cfg := sandbox.InactivityConfig{
		Timeout:          10 * time.Minute,
		Extension:        5 * time.Minute,
		MinCheckInterval: 30 * time.Second,
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("schedule for terminal states (stopped)", func(t *testing.T) {
		t.Parallel()
		state := sandbox.InactivityState{
			LastActivity: now.Add(-cfg.Timeout - time.Minute),
			Status:       sandbox.StateStopped,
		}
		got := sandbox.EvaluateInactivityTimeout(state, cfg, now)
		assertSchedule(t, got, cfg.MinCheckInterval)
	})

	t.Run("schedule for terminal states (failed)", func(t *testing.T) {
		t.Parallel()
		state := sandbox.InactivityState{LastActivity: now.Add(-cfg.Timeout - time.Minute), Status: sandbox.StateFailed}
		got := sandbox.EvaluateInactivityTimeout(state, cfg, now)
		assertSchedule(t, got, cfg.MinCheckInterval)
	})

	t.Run("schedule for terminal states (stale)", func(t *testing.T) {
		t.Parallel()
		state := sandbox.InactivityState{LastActivity: now.Add(-cfg.Timeout - time.Minute), Status: sandbox.StateStale}
		got := sandbox.EvaluateInactivityTimeout(state, cfg, now)
		assertSchedule(t, got, cfg.MinCheckInterval)
	})

	t.Run("schedule when no lastActivity", func(t *testing.T) {
		t.Parallel()
		state := sandbox.InactivityState{Status: sandbox.StateReady}
		got := sandbox.EvaluateInactivityTimeout(state, cfg, now)
		assertSchedule(t, got, cfg.MinCheckInterval)
	})

	t.Run("timeout when inactivity exceeds threshold with no clients", func(t *testing.T) {
		t.Parallel()
		state := sandbox.InactivityState{
			LastActivity: now.Add(-cfg.Timeout - time.Second),
			Status:       sandbox.StateReady,
		}
		got := sandbox.EvaluateInactivityTimeout(state, cfg, now)
		if got.Kind != sandbox.InactivityActionTimeout {
			t.Fatalf("Kind = %s, want timeout", got.Kind)
		}
		if !got.ShouldSnapshot {
			t.Error("ShouldSnapshot = false, want true")
		}
	})

	t.Run("extend when threshold exceeded but clients connected", func(t *testing.T) {
		t.Parallel()
		state := sandbox.InactivityState{
			LastActivity:         now.Add(-cfg.Timeout - time.Second),
			Status:               sandbox.StateReady,
			ConnectedClientCount: 2,
		}
		got := sandbox.EvaluateInactivityTimeout(state, cfg, now)
		if got.Kind != sandbox.InactivityActionExtend {
			t.Fatalf("Kind = %s, want extend", got.Kind)
		}
		if got.Extension != cfg.Extension {
			t.Errorf("Extension = %v, want %v", got.Extension, cfg.Extension)
		}
		if !got.ShouldWarn {
			t.Error("ShouldWarn = false, want true")
		}
	})

	t.Run("schedule with the correct remaining time", func(t *testing.T) {
		t.Parallel()
		inactiveTime := 5 * time.Minute
		state := sandbox.InactivityState{LastActivity: now.Add(-inactiveTime), Status: sandbox.StateReady}
		got := sandbox.EvaluateInactivityTimeout(state, cfg, now)
		assertSchedule(t, got, cfg.Timeout-inactiveTime)
	})

	t.Run("handles the minimum check interval", func(t *testing.T) {
		t.Parallel()
		// 9m50s -- very close to timeout; remaining (10s) is below the
		// 30s min interval, so the min interval wins.
		inactiveTime := cfg.Timeout - 10*time.Second
		state := sandbox.InactivityState{LastActivity: now.Add(-inactiveTime), Status: sandbox.StateReady}
		got := sandbox.EvaluateInactivityTimeout(state, cfg, now)
		assertSchedule(t, got, cfg.MinCheckInterval)
	})

	t.Run("only applies to the live steady state (ready)", func(t *testing.T) {
		t.Parallel()
		state := sandbox.InactivityState{
			LastActivity: now.Add(-cfg.Timeout - time.Minute),
			Status:       sandbox.StateSpawning, // boot in progress, not ready
		}
		got := sandbox.EvaluateInactivityTimeout(state, cfg, now)
		assertSchedule(t, got, cfg.MinCheckInterval)
	})

	t.Run("schedule when isProcessing, even past the inactivity threshold", func(t *testing.T) {
		t.Parallel()
		state := sandbox.InactivityState{
			LastActivity: now.Add(-cfg.Timeout - time.Minute),
			Status:       sandbox.StateReady,
			IsProcessing: true,
		}
		got := sandbox.EvaluateInactivityTimeout(state, cfg, now)
		assertSchedule(t, got, cfg.MinCheckInterval)
	})
}

func assertSchedule(t *testing.T, got sandbox.InactivityAction, wantNextCheck time.Duration) {
	t.Helper()
	if got.Kind != sandbox.InactivityActionSchedule {
		t.Fatalf("Kind = %s, want schedule (full action: %+v)", got.Kind, got)
	}
	if got.NextCheck != wantNextCheck {
		t.Errorf("NextCheck = %v, want %v", got.NextCheck, wantNextCheck)
	}
}
