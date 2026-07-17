package sandbox_test

import (
	"testing"
	"time"

	"github.com/khazaddev/narvi/internal/domain/sandbox"
)

var livenessCfg = sandbox.LivenessConfig{
	FirstConnectBudget:    240 * time.Second,
	SteadyHeartbeatBudget: 90 * time.Second,
}

// TestEvaluateConnectingTimeout exercises EvaluateConnectingTimeout's
// scenarios against Narvi's two-budget model (no separate "reconnect"
// budget -- SteadyHeartbeatBudget plays that role) and three-way boot phase
// (Spawning, Connecting, Booting).
func TestEvaluateConnectingTimeout(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("not timed out for a non-boot-phase status", func(t *testing.T) {
		t.Parallel()
		got := sandbox.EvaluateConnectingTimeout(sandbox.StateReady, now.Add(-200*time.Second), time.Time{}, livenessCfg, now)
		if got.IsTimedOut || got.Elapsed != 0 {
			t.Errorf("got %+v, want IsTimedOut=false Elapsed=0", got)
		}
	})

	t.Run("not timed out within the first-connect window", func(t *testing.T) {
		t.Parallel()
		createdAt := now.Add(-200 * time.Second) // within the 240s budget
		got := sandbox.EvaluateConnectingTimeout(sandbox.StateConnecting, createdAt, time.Time{}, livenessCfg, now)
		if got.IsTimedOut {
			t.Error("IsTimedOut = true, want false")
		}
		if got.Elapsed != 200*time.Second {
			t.Errorf("Elapsed = %v, want 200s", got.Elapsed)
		}
	})

	t.Run("timed out past the first-connect budget (no sign of life)", func(t *testing.T) {
		t.Parallel()
		createdAt := now.Add(-250 * time.Second) // past the 240s budget
		got := sandbox.EvaluateConnectingTimeout(sandbox.StateConnecting, createdAt, time.Time{}, livenessCfg, now)
		if !got.IsTimedOut {
			t.Error("IsTimedOut = false, want true")
		}
	})

	t.Run("timed out at the exact first-connect boundary (>=)", func(t *testing.T) {
		t.Parallel()
		createdAt := now.Add(-livenessCfg.FirstConnectBudget)
		got := sandbox.EvaluateConnectingTimeout(sandbox.StateConnecting, createdAt, time.Time{}, livenessCfg, now)
		if !got.IsTimedOut {
			t.Error("IsTimedOut = false, want true (exact boundary is >=)")
		}
	})

	t.Run("stuck in spawning past first-connect timeout (interrupted spawn)", func(t *testing.T) {
		t.Parallel()
		createdAt := now.Add(-250 * time.Second)
		got := sandbox.EvaluateConnectingTimeout(sandbox.StateSpawning, createdAt, time.Time{}, livenessCfg, now)
		if !got.IsTimedOut {
			t.Error("IsTimedOut = false, want true")
		}
	})

	t.Run("stuck in booting past first-connect timeout (Narvi's own third phase)", func(t *testing.T) {
		t.Parallel()
		createdAt := now.Add(-250 * time.Second)
		got := sandbox.EvaluateConnectingTimeout(sandbox.StateBooting, createdAt, time.Time{}, livenessCfg, now)
		if !got.IsTimedOut {
			t.Error("IsTimedOut = false, want true")
		}
	})

	t.Run("not timed out for spawning within the timeout window", func(t *testing.T) {
		t.Parallel()
		got := sandbox.EvaluateConnectingTimeout(sandbox.StateSpawning, now.Add(-60*time.Second), time.Time{}, livenessCfg, now)
		if got.IsTimedOut {
			t.Error("IsTimedOut = true, want false")
		}
	})

	t.Run("ignores every non-boot-phase status", func(t *testing.T) {
		t.Parallel()
		old := now.Add(-999_999 * time.Second)
		for _, status := range []sandbox.State{
			sandbox.StatePending, sandbox.StateReady, sandbox.StateSnapshotting,
			sandbox.StateSuspect, sandbox.StateStopped, sandbox.StateFailed, sandbox.StateStale,
		} {
			got := sandbox.EvaluateConnectingTimeout(status, old, time.Time{}, livenessCfg, now)
			if got.IsTimedOut {
				t.Errorf("status %s: IsTimedOut = true, want false", status)
			}
		}
	})

	t.Run("measures from last sign of life, not creation, during a long boot", func(t *testing.T) {
		t.Parallel()
		createdAt := now.Add(-300 * time.Second) // created 5 min ago (slow setup.sh)
		lastSeenAt := now.Add(-10 * time.Second) // but pinged 10s ago
		got := sandbox.EvaluateConnectingTimeout(sandbox.StateConnecting, createdAt, lastSeenAt, livenessCfg, now)
		if got.IsTimedOut {
			t.Error("IsTimedOut = true, want false")
		}
		if got.Elapsed != 10*time.Second {
			t.Errorf("Elapsed = %v, want 10s", got.Elapsed)
		}
	})

	t.Run("times out when boot-progress pings stop for the full steady budget", func(t *testing.T) {
		t.Parallel()
		createdAt := now.Add(-300 * time.Second)
		lastSeenAt := now.Add(-livenessCfg.SteadyHeartbeatBudget) // last ping exactly one budget ago
		got := sandbox.EvaluateConnectingTimeout(sandbox.StateConnecting, createdAt, lastSeenAt, livenessCfg, now)
		if !got.IsTimedOut {
			t.Error("IsTimedOut = false, want true")
		}
		if got.Elapsed != livenessCfg.SteadyHeartbeatBudget {
			t.Errorf("Elapsed = %v, want %v", got.Elapsed, livenessCfg.SteadyHeartbeatBudget)
		}
	})

	t.Run("falls back to creation time when no progress reported yet", func(t *testing.T) {
		t.Parallel()
		createdAt := now.Add(-130 * time.Second)

		// A lastSeenAt older than createdAt (or absent) must not extend the window.
		if !sandbox.EvaluateConnectingTimeout(sandbox.StateConnecting, createdAt, createdAt.Add(-50*time.Second), livenessCfg, now).IsTimedOut {
			t.Error("older lastSeenAt: IsTimedOut = false, want true")
		}
		if got := sandbox.EvaluateConnectingTimeout(sandbox.StateConnecting, createdAt, time.Time{}, livenessCfg, now).Elapsed; got != 130*time.Second {
			t.Errorf("Elapsed = %v, want 130s", got)
		}
	})

	t.Run("applies the shorter steady budget once a sign of life has arrived", func(t *testing.T) {
		t.Parallel()
		// Pinged 80s ago: within the 90s steady budget, still alive.
		if sandbox.EvaluateConnectingTimeout(sandbox.StateConnecting, now.Add(-300*time.Second), now.Add(-80*time.Second), livenessCfg, now).IsTimedOut {
			t.Error("pinged 80s ago: IsTimedOut = true, want false")
		}
		// Pinged 100s ago: past the 90s steady budget, fast-fail preserved.
		if !sandbox.EvaluateConnectingTimeout(sandbox.StateConnecting, now.Add(-300*time.Second), now.Add(-100*time.Second), livenessCfg, now).IsTimedOut {
			t.Error("pinged 100s ago: IsTimedOut = false, want true")
		}
	})

	t.Run("picks the budget from lastSeenAt presence (first-connect vs steady crossover)", func(t *testing.T) {
		t.Parallel()
		createdAt := now.Add(-130 * time.Second) // between the 90s and 240s budgets

		// No sign of life yet -> first-connect budget (240s) -> still booting.
		if sandbox.EvaluateConnectingTimeout(sandbox.StateConnecting, createdAt, time.Time{}, livenessCfg, now).IsTimedOut {
			t.Error("no sign of life: IsTimedOut = true, want false")
		}
		// Has pinged 130s ago -> steady budget (90s) -> timed out.
		if !sandbox.EvaluateConnectingTimeout(sandbox.StateConnecting, createdAt, createdAt, livenessCfg, now).IsTimedOut {
			t.Error("pinged 130s ago: IsTimedOut = false, want true")
		}
	})
}

// TestEvaluateHeartbeatHealth exercises EvaluateHeartbeatHealth's scenarios,
// with the threshold sourced from LivenessConfig.SteadyHeartbeatBudget (the
// same field EvaluateConnectingTimeout uses for its "has pinged" branch)
// rather than a standalone heartbeat config.
func TestEvaluateHeartbeatHealth(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("not stale when no heartbeat recorded", func(t *testing.T) {
		t.Parallel()
		got := sandbox.EvaluateHeartbeatHealth(time.Time{}, livenessCfg, now)
		if got.IsStale || got.Age != 0 {
			t.Errorf("got %+v, want IsStale=false Age=0", got)
		}
	})

	t.Run("not stale when heartbeat is recent", func(t *testing.T) {
		t.Parallel()
		got := sandbox.EvaluateHeartbeatHealth(now.Add(-30*time.Second), livenessCfg, now)
		if got.IsStale || got.Age != 0 {
			t.Errorf("got %+v, want IsStale=false Age=0", got)
		}
	})

	t.Run("stale when heartbeat exceeds the steady budget", func(t *testing.T) {
		t.Parallel()
		got := sandbox.EvaluateHeartbeatHealth(now.Add(-100*time.Second), livenessCfg, now)
		if !got.IsStale {
			t.Error("IsStale = false, want true")
		}
		if got.Age != 100*time.Second {
			t.Errorf("Age = %v, want 100s", got.Age)
		}
	})

	t.Run("returns the correct age", func(t *testing.T) {
		t.Parallel()
		got := sandbox.EvaluateHeartbeatHealth(now.Add(-150*time.Second), livenessCfg, now)
		if !got.IsStale || got.Age != 150*time.Second {
			t.Errorf("got %+v, want IsStale=true Age=150s", got)
		}
	})

	t.Run("boundary timing: exactly at the budget is not stale (> vs >=)", func(t *testing.T) {
		t.Parallel()
		got := sandbox.EvaluateHeartbeatHealth(now.Add(-livenessCfg.SteadyHeartbeatBudget), livenessCfg, now)
		if got.IsStale {
			t.Error("IsStale = true, want false (exact boundary is >, not >=)")
		}
	})

	t.Run("boundary timing: just past the budget is stale", func(t *testing.T) {
		t.Parallel()
		lastSeenAt := now.Add(-livenessCfg.SteadyHeartbeatBudget - time.Millisecond)
		got := sandbox.EvaluateHeartbeatHealth(lastSeenAt, livenessCfg, now)
		if !got.IsStale {
			t.Error("IsStale = false, want true")
		}
	})
}
