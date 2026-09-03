package turn_test

import (
	"testing"
	"time"

	"github.com/narvidev/narvi/internal/domain/turn"
)

// TestEvaluateTurnDeadline covers: comfortably within the deadline, past
// the deadline, and the exact boundary (>=).
func TestEvaluateTurnDeadline(t *testing.T) {
	t.Parallel()

	cfg := turn.DeadlineConfig{Deadline: 60 * time.Minute}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("well within the deadline", func(t *testing.T) {
		t.Parallel()
		startedAt := now.Add(-10 * time.Minute)
		got := turn.EvaluateTurnDeadline(startedAt, now, cfg)
		if got.IsTimedOut {
			t.Error("IsTimedOut = true, want false")
		}
		if got.Elapsed != 10*time.Minute {
			t.Errorf("Elapsed = %v, want 10m", got.Elapsed)
		}
	})

	t.Run("past the deadline", func(t *testing.T) {
		t.Parallel()
		startedAt := now.Add(-90 * time.Minute)
		got := turn.EvaluateTurnDeadline(startedAt, now, cfg)
		if !got.IsTimedOut {
			t.Error("IsTimedOut = false, want true")
		}
		if got.Elapsed != 90*time.Minute {
			t.Errorf("Elapsed = %v, want 90m", got.Elapsed)
		}
	})

	t.Run("exact boundary is timed out (>=)", func(t *testing.T) {
		t.Parallel()
		startedAt := now.Add(-cfg.Deadline)
		got := turn.EvaluateTurnDeadline(startedAt, now, cfg)
		if !got.IsTimedOut {
			t.Error("IsTimedOut = false, want true (exact boundary is >=)")
		}
	})
}
