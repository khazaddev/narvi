package review_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/review"
)

// TestCoverageFloor is exhaustive over every legal TestsCoverageState value
// plus the zero value and an arbitrary unrecognized string, proving
// CoverageFloor's own documented fail-conservative policy: an unrecognized
// state ranks identically to TestsCoverageStateInsufficient, never as
// permissive as TestsCoverageStateAdequate/Skipped.
func TestCoverageFloor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state review.TestsCoverageState
		want  review.Shippable
	}{
		{"adequate imposes no floor", review.TestsCoverageStateAdequate, review.ShippableAuto},
		{"skipped (deliberate) imposes no floor", review.TestsCoverageStateSkipped, review.ShippableAuto},
		{"insufficient raises to needs_human", review.TestsCoverageStateInsufficient, review.ShippableNeedsHuman},
		{"zero value fails conservative (needs_human, matching insufficient)", review.TestsCoverageState(""), review.ShippableNeedsHuman},
		{"unrecognized value fails conservative (needs_human, matching insufficient)", review.TestsCoverageState("bogus"), review.ShippableNeedsHuman},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := review.CoverageFloor(tc.state)
			if got != tc.want {
				t.Errorf("CoverageFloor(%q) = %s, want %s", tc.state, got, tc.want)
			}
		})
	}
}
