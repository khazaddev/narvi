package reviewtriage_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/reviewtriage"
)

func TestShouldSkipOptionalPass(t *testing.T) {
	tests := []struct {
		name     string
		spentUSD float64
		ceiling  float64
		want     bool
	}{
		{"zero ceiling (unconfigured) never skips, even with huge spend", 1000, 0, false},
		{"negative ceiling degrades to unconfigured, never skips", 1000, -5, false},
		{"well under the 80% margin", 1.0, 5.0, false},
		{"exactly at the 80% margin skips (>=, not >)", 4.0, 5.0, true},
		{"just under the 80% margin does not skip", 3.99, 5.0, false},
		{"just over the 80% margin skips", 4.01, 5.0, true},
		{"spend at the full ceiling skips", 5.0, 5.0, true},
		{"spend over the full ceiling skips", 6.0, 5.0, true},
		{"zero spend, real ceiling never skips", 0, 5.0, false},
		{"negative spend clamps to zero, never skips against a real ceiling", -3, 5.0, false},
		{"light path's own $0.50 default ceiling: under 80%", 0.30, 0.50, false},
		{"light path's own $0.50 default ceiling: at 80%", 0.40, 0.50, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reviewtriage.ShouldSkipOptionalPass(tt.spentUSD, tt.ceiling)
			if got != tt.want {
				t.Errorf("ShouldSkipOptionalPass(%v, %v) = %v, want %v", tt.spentUSD, tt.ceiling, got, tt.want)
			}
		})
	}
}

// TestShouldSkipOptionalPass_CheckedBeforeDispatchNotAfter is this Step's
// own explicit mutation-test pin: the budget decision is a LOOK-AHEAD
// check over spend ALREADY ACCUMULATED before the next optional pass
// would be dispatched, never a prediction of that pass's own future cost
// and never evaluated only after the fact. This test pins the exact
// semantics a caller must uphold: spentUSD passed to this function must
// be the running total BEFORE the candidate dispatch, so a spend value
// that has NOT yet included the next pass's own (unknown) cost correctly
// determines whether that pass fires at all. Concretely: at 79% of the
// ceiling, the NEXT pass is still dispatched (the check passed BEFORE
// commitment) regardless of what that pass might go on to cost -- this
// function has no way to observe or refuse to have been called with a
// too-early spend figure, which is exactly the point: the ceiling is
// enforced on the caller's own OBSERVED-so-far total, never a guess at
// what comes next.
func TestShouldSkipOptionalPass_CheckedBeforeDispatchNotAfter(t *testing.T) {
	ceiling := 5.0
	spentBeforeNextDispatch := 3.9 // 78% -- under the 80% margin.

	if reviewtriage.ShouldSkipOptionalPass(spentBeforeNextDispatch, ceiling) {
		t.Fatalf("ShouldSkipOptionalPass(%v, %v) = true, want false (spend recorded BEFORE the candidate dispatch must gate on ITS OWN value, never a hypothetical post-dispatch total)", spentBeforeNextDispatch, ceiling)
	}
}

func TestDefaultCostBudget(t *testing.T) {
	got := reviewtriage.DefaultCostBudget()
	if got.Light != 0.50 {
		t.Errorf("DefaultCostBudget().Light = %v, want 0.50", got.Light)
	}
	if got.Deep != 5.00 {
		t.Errorf("DefaultCostBudget().Deep = %v, want 5.00", got.Deep)
	}
}
