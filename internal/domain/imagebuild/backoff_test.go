package imagebuild_test

import (
	"testing"
	"time"

	"github.com/khazaddev/narvi/internal/domain/imagebuild"
)

// TestEvaluateBackoff_ExponentialGrowthCappedAtMax proves §3.5's one hard
// requirement ("retry with exponential backoff (not fixed 30 min)"): the
// delay strictly grows with each additional consecutive failure, then
// plateaus once it reaches cfg.MaxDelay -- it never regresses and never
// exceeds the cap.
func TestEvaluateBackoff_ExponentialGrowthCappedAtMax(t *testing.T) {
	t.Parallel()

	cfg := imagebuild.BackoffConfig{
		BaseDelay: 1 * time.Minute,
		MaxDelay:  30 * time.Minute,
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		attemptCount int
		wantDelay    time.Duration
	}{
		{1, 1 * time.Minute},
		{2, 2 * time.Minute},
		{3, 4 * time.Minute},
		{4, 8 * time.Minute},
		{5, 16 * time.Minute},
		{6, 30 * time.Minute}, // 32min would exceed the 30min cap
		{7, 30 * time.Minute}, // plateaus, does not keep growing
		{20, 30 * time.Minute},
	}

	var previousDelay time.Duration
	for _, tc := range tests {
		got := imagebuild.EvaluateBackoff(tc.attemptCount, cfg, now)
		gotDelay := got.NextRetryAt.Sub(now)

		if gotDelay != tc.wantDelay {
			t.Errorf("attemptCount=%d: delay = %v, want %v", tc.attemptCount, gotDelay, tc.wantDelay)
		}
		if gotDelay > cfg.MaxDelay {
			t.Errorf("attemptCount=%d: delay %v exceeds MaxDelay %v", tc.attemptCount, gotDelay, cfg.MaxDelay)
		}
		if tc.attemptCount > 1 && gotDelay < previousDelay {
			t.Errorf("attemptCount=%d: delay %v is LESS than the previous attempt's delay %v -- backoff must never shrink",
				tc.attemptCount, gotDelay, previousDelay)
		}
		previousDelay = gotDelay
	}
}

// TestEvaluateBackoff_NeverConstantAcrossFirstFewFailures is a direct,
// narrow proof of §3.5's own explicit language ("not fixed 30 min"): the
// delay after the first failure must differ from the delay after the
// second and third, i.e. it is NOT a fixed interval from the very first
// attempt (which a naive "just reuse the old cadence" implementation could
// otherwise satisfy by accident for a narrow attempt-count range).
func TestEvaluateBackoff_NeverConstantAcrossFirstFewFailures(t *testing.T) {
	t.Parallel()

	cfg := imagebuild.BackoffConfig{BaseDelay: 1 * time.Minute, MaxDelay: 30 * time.Minute}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	d1 := imagebuild.EvaluateBackoff(1, cfg, now).NextRetryAt.Sub(now)
	d2 := imagebuild.EvaluateBackoff(2, cfg, now).NextRetryAt.Sub(now)
	d3 := imagebuild.EvaluateBackoff(3, cfg, now).NextRetryAt.Sub(now)

	if d1 == d2 || d2 == d3 {
		t.Fatalf("backoff delay stayed constant across early attempts (d1=%v d2=%v d3=%v) -- §3.5 requires exponential, not fixed", d1, d2, d3)
	}
}

// TestEvaluateBackoff_AttemptCountBelowOneTreatedAsOne proves the
// defensive floor: there is no such thing as a "0th" failed attempt, so an
// invalid/zero attemptCount behaves exactly like 1.
func TestEvaluateBackoff_AttemptCountBelowOneTreatedAsOne(t *testing.T) {
	t.Parallel()

	cfg := imagebuild.BackoffConfig{BaseDelay: 1 * time.Minute, MaxDelay: 30 * time.Minute}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	want := imagebuild.EvaluateBackoff(1, cfg, now)
	for _, attemptCount := range []int{0, -1, -100} {
		got := imagebuild.EvaluateBackoff(attemptCount, cfg, now)
		if !got.NextRetryAt.Equal(want.NextRetryAt) {
			t.Errorf("attemptCount=%d: NextRetryAt = %v, want %v (same as attemptCount=1)", attemptCount, got.NextRetryAt, want.NextRetryAt)
		}
	}
}

// TestEvaluateBackoff_StreakAlertThreshold proves the streak alert fires
// starting exactly at ImageBuildStreakThreshold consecutive failures, and
// not before -- the one behavior the integration test (e) also proves
// end-to-end against real Postgres.
func TestEvaluateBackoff_StreakAlertThreshold(t *testing.T) {
	t.Parallel()

	cfg := imagebuild.BackoffConfig{BaseDelay: 1 * time.Minute, MaxDelay: 30 * time.Minute}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for attemptCount := 1; attemptCount < imagebuild.ImageBuildStreakThreshold; attemptCount++ {
		got := imagebuild.EvaluateBackoff(attemptCount, cfg, now)
		if got.StreakAlert {
			t.Errorf("attemptCount=%d (below threshold %d): StreakAlert = true, want false", attemptCount, imagebuild.ImageBuildStreakThreshold)
		}
	}

	for attemptCount := imagebuild.ImageBuildStreakThreshold; attemptCount <= imagebuild.ImageBuildStreakThreshold+5; attemptCount++ {
		got := imagebuild.EvaluateBackoff(attemptCount, cfg, now)
		if !got.StreakAlert {
			t.Errorf("attemptCount=%d (at/above threshold %d): StreakAlert = false, want true", attemptCount, imagebuild.ImageBuildStreakThreshold)
		}
	}
}

// TestImageBuildStreakThreshold_MatchesEstablishedConvention pins the
// constant's own value against this codebase's already-established "3
// consecutive failures" convention (sandbox.CircuitBreakerThreshold, §3.5's
// automations auto-pause rule) -- see backoff.go's own doc comment for the
// reasoning.
func TestImageBuildStreakThreshold_MatchesEstablishedConvention(t *testing.T) {
	t.Parallel()

	if imagebuild.ImageBuildStreakThreshold != 3 {
		t.Errorf("ImageBuildStreakThreshold = %d, want 3 (matching sandbox.CircuitBreakerThreshold and §3.5's automations convention)", imagebuild.ImageBuildStreakThreshold)
	}
}
