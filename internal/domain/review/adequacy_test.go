package review_test

import (
	"testing"

	"github.com/narvidev/narvi/internal/domain/review"
)

// TestAdequacyFloor is exhaustive over every legal DescriptionAdequacy
// value plus the zero value and an arbitrary unrecognized string, proving
// AdequacyFloor's own documented fail-conservative policy: an unrecognized
// state ranks identically to DescriptionAdequacyMisleading, this floor's
// strictest legal Shippable value -- and that "ok"/"drift" both impose no
// floor at all, matching §26.2's own "misleading floors Shippable... "
// wording precisely (drift alone is deliberately NOT a floor trigger).
func TestAdequacyFloor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		adequacy  review.DescriptionAdequacy
		wantFloor review.Shippable
	}{
		{"ok imposes no floor", review.DescriptionAdequacyOK, review.ShippableAuto},
		{"drift imposes no floor", review.DescriptionAdequacyDrift, review.ShippableAuto},
		{"misleading raises to needs_human", review.DescriptionAdequacyMisleading, review.ShippableNeedsHuman},
		{"zero value fails conservative (needs_human, matching misleading)", review.DescriptionAdequacy(""), review.ShippableNeedsHuman},
		{"unrecognized value fails conservative (needs_human, matching misleading)", review.DescriptionAdequacy("bogus"), review.ShippableNeedsHuman},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := review.AdequacyFloor(tc.adequacy)
			if got != tc.wantFloor {
				t.Errorf("AdequacyFloor(%q) = %s, want %s", tc.adequacy, got, tc.wantFloor)
			}
		})
	}
}
