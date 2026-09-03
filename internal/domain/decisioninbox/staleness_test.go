package decisioninbox_test

import (
	"testing"
	"time"

	"github.com/narvidev/narvi/internal/domain/decisioninbox"
)

func TestIsStale(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	const threshold = 48 * time.Hour

	tests := []struct {
		name           string
		enteredQueueAt time.Time
		want           bool
	}{
		{"well within threshold", now.Add(-1 * time.Hour), false},
		{"exactly at threshold is not yet stale", now.Add(-threshold), false},
		{"just past threshold is stale", now.Add(-threshold - time.Second), true},
		{"far past threshold is stale", now.Add(-30 * 24 * time.Hour), true},
		{"zero time is never stale (unknown age)", time.Time{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := decisioninbox.IsStale(tc.enteredQueueAt, now, threshold); got != tc.want {
				t.Errorf("IsStale(%v, now, %v) = %v, want %v", tc.enteredQueueAt, threshold, got, tc.want)
			}
		})
	}
}

func TestAge(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)

	if got := decisioninbox.Age(now.Add(-2*time.Hour), now); got != 2*time.Hour {
		t.Errorf("Age() = %v, want 2h", got)
	}
	if got := decisioninbox.Age(time.Time{}, now); got != 0 {
		t.Errorf("Age(zero) = %v, want 0", got)
	}
}
