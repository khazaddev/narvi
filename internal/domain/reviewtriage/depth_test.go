package reviewtriage_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/reviewtriage"
)

func TestFloor(t *testing.T) {
	tests := []struct {
		name       string
		fresh      reviewtriage.ReviewDepth
		prior      reviewtriage.ReviewDepth
		wantResult reviewtriage.ReviewDepth
	}{
		{"light fresh, no prior", reviewtriage.DepthLight, "", reviewtriage.DepthLight},
		{"deep fresh, no prior", reviewtriage.DepthDeep, "", reviewtriage.DepthDeep},
		{"light fresh, light prior", reviewtriage.DepthLight, reviewtriage.DepthLight, reviewtriage.DepthLight},
		{"light fresh, deep prior floors to deep", reviewtriage.DepthLight, reviewtriage.DepthDeep, reviewtriage.DepthDeep},
		{"deep fresh, light prior stays deep", reviewtriage.DepthDeep, reviewtriage.DepthLight, reviewtriage.DepthDeep},
		{"deep fresh, deep prior stays deep", reviewtriage.DepthDeep, reviewtriage.DepthDeep, reviewtriage.DepthDeep},
		{"unrecognized prior never wins over a deep fresh", reviewtriage.DepthDeep, reviewtriage.ReviewDepth("garbled"), reviewtriage.DepthDeep},
		{"unrecognized prior never forces a light fresh to deep", reviewtriage.DepthLight, reviewtriage.ReviewDepth("garbled"), reviewtriage.DepthLight},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reviewtriage.Floor(tt.fresh, tt.prior)
			if got != tt.wantResult {
				t.Errorf("Floor(%q, %q) = %q, want %q", tt.fresh, tt.prior, got, tt.wantResult)
			}
		})
	}
}

// TestFloor_ReReviewNeverDowngrades is the explicit §24 pin the task's
// own process requirements name: "a light-looking delta on an
// already-deep PR must still route deep". Mutating Floor's own `if
// rank(prior) > rank(fresh)` into `>=` or `<` must fail this test.
func TestFloor_ReReviewNeverDowngrades(t *testing.T) {
	got := reviewtriage.Floor(reviewtriage.DepthLight, reviewtriage.DepthDeep)
	if got != reviewtriage.DepthDeep {
		t.Fatalf("a PR previously routed deep must never float back to light on a smaller delta: got %q", got)
	}
}
