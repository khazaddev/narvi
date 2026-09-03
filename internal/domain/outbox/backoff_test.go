package outbox_test

import (
	"testing"
	"time"

	"github.com/narvidev/narvi/internal/domain/outbox"
)

func TestEvaluateBackoff(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cfg := outbox.BackoffConfig{
		BaseDelay: 30 * time.Second,
		MaxDelay:  5 * time.Minute,
	}

	tests := []struct {
		name           string
		attemptCount   int
		wantDelay      time.Duration
		wantDeadLetter bool
	}{
		{
			name:         "first failure schedules base delay",
			attemptCount: 1,
			wantDelay:    30 * time.Second,
		},
		{
			name:         "second failure doubles",
			attemptCount: 2,
			wantDelay:    1 * time.Minute,
		},
		{
			name:         "third failure doubles again",
			attemptCount: 3,
			wantDelay:    2 * time.Minute,
		},
		{
			name:         "fourth failure doubles again",
			attemptCount: 4,
			wantDelay:    4 * time.Minute,
		},
		{
			name:         "fifth failure caps at max delay",
			attemptCount: 5,
			wantDelay:    5 * time.Minute,
		},
		{
			name:         "sixth failure plateaus at max delay",
			attemptCount: 6,
			wantDelay:    5 * time.Minute,
		},
		{
			name:         "attemptCount below 1 treated as 1",
			attemptCount: 0,
			wantDelay:    30 * time.Second,
		},
		{
			name:           "attemptCount at MaxAttempts dead-letters",
			attemptCount:   outbox.MaxAttempts,
			wantDeadLetter: true,
		},
		{
			name:           "attemptCount beyond MaxAttempts dead-letters",
			attemptCount:   outbox.MaxAttempts + 5,
			wantDeadLetter: true,
		},
		{
			name:         "attemptCount one below MaxAttempts still retries",
			attemptCount: outbox.MaxAttempts - 1,
			wantDelay:    5 * time.Minute,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := outbox.EvaluateBackoff(tc.attemptCount, cfg, now)

			if got.DeadLetter != tc.wantDeadLetter {
				t.Fatalf("EvaluateBackoff(%d).DeadLetter = %v, want %v", tc.attemptCount, got.DeadLetter, tc.wantDeadLetter)
			}
			if tc.wantDeadLetter {
				return
			}
			wantNextRetryAt := now.Add(tc.wantDelay)
			if !got.NextRetryAt.Equal(wantNextRetryAt) {
				t.Fatalf("EvaluateBackoff(%d).NextRetryAt = %v, want %v", tc.attemptCount, got.NextRetryAt, wantNextRetryAt)
			}
		})
	}
}
