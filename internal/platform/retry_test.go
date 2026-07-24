package platform_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/khazaddev/narvi/internal/platform"
)

var errBoom = errors.New("boom")

func TestRetry_SucceedsFirstTry(t *testing.T) {
	t.Parallel()

	calls := 0
	err := platform.Retry(context.Background(), 3, time.Millisecond, 10*time.Millisecond, func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("Retry() = %v, want nil", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry needed)", calls)
	}
}

func TestRetry_SucceedsAfterTransientFailures(t *testing.T) {
	t.Parallel()

	calls := 0
	err := platform.Retry(context.Background(), 5, time.Millisecond, 10*time.Millisecond, func() error {
		calls++
		if calls < 3 {
			return errBoom
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Retry() = %v, want nil", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestRetry_ExhaustsAttemptsReturnsLastError(t *testing.T) {
	t.Parallel()

	calls := 0
	err := platform.Retry(context.Background(), 3, time.Millisecond, 10*time.Millisecond, func() error {
		calls++
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("Retry() = %v, want errBoom", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want exactly 3 (attempts exhausted)", calls)
	}
}

func TestRetry_PermanentErrorStopsImmediately(t *testing.T) {
	t.Parallel()

	calls := 0
	err := platform.Retry(context.Background(), 5, time.Millisecond, 10*time.Millisecond, func() error {
		calls++
		return platform.Permanent(errBoom)
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("Retry() = %v, want errBoom", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (permanent error must not retry)", calls)
	}
}

func TestRetry_ContextCanceledWhileWaitingStopsRetrying(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := platform.Retry(ctx, 5, 50*time.Millisecond, 200*time.Millisecond, func() error {
		calls++
		if calls == 1 {
			cancel()
		}
		return errBoom
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Retry() = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (must stop waiting once ctx is canceled)", calls)
	}
}

func TestRetry_ZeroOrNegativeAttemptsStillCallsOnce(t *testing.T) {
	t.Parallel()

	for _, attempts := range []int{0, -1} {
		calls := 0
		err := platform.Retry(context.Background(), attempts, time.Millisecond, time.Millisecond, func() error {
			calls++
			return errBoom
		})
		if !errors.Is(err, errBoom) {
			t.Fatalf("attempts=%d: Retry() = %v, want errBoom", attempts, err)
		}
		if calls != 1 {
			t.Errorf("attempts=%d: calls = %d, want 1", attempts, calls)
		}
	}
}

func TestRetry_DelayDoublesAndCapsAtMax(t *testing.T) {
	t.Parallel()

	var timestamps []time.Time
	err := platform.Retry(context.Background(), 4, 5*time.Millisecond, 12*time.Millisecond, func() error {
		timestamps = append(timestamps, time.Now())
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("Retry() = %v, want errBoom", err)
	}
	if len(timestamps) != 4 {
		t.Fatalf("len(timestamps) = %d, want 4", len(timestamps))
	}

	// Deliberately loose bounds (this is real wall-clock timing, not a
	// fake clock) -- just proves the gaps are non-decreasing and roughly
	// match the doubling-capped-at-max schedule (5ms, 10ms, 12ms) rather
	// than e.g. a fixed or shrinking delay.
	gap1 := timestamps[1].Sub(timestamps[0])
	gap2 := timestamps[2].Sub(timestamps[1])
	gap3 := timestamps[3].Sub(timestamps[2])

	if gap1 < 5*time.Millisecond {
		t.Errorf("gap1 = %v, want >= 5ms", gap1)
	}
	if gap2 < gap1 {
		t.Errorf("gap2 = %v, want >= gap1 = %v (should double)", gap2, gap1)
	}
	if gap3 < 12*time.Millisecond {
		t.Errorf("gap3 = %v, want >= 12ms (capped delay still applies)", gap3)
	}
}
