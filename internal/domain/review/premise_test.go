package review_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/review"
)

// TestPremiseFloor is exhaustive over every legal PremiseState value plus
// the zero value and an arbitrary unrecognized string, proving
// PremiseFloor's own documented fail-conservative policy: an unrecognized
// state ranks identically to PremiseStateNotAPR, this package's strictest
// legal Shippable value.
func TestPremiseFloor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state review.PremiseState
		want  review.Shippable
	}{
		{"ok imposes no floor", review.PremiseStateOK, review.ShippableAuto},
		{"questionable raises to needs_human", review.PremiseStateQuestionable, review.ShippableNeedsHuman},
		{"not_a_pr raises to block", review.PremiseStateNotAPR, review.ShippableBlock},
		{"zero value fails conservative (block, matching not_a_pr)", review.PremiseState(""), review.ShippableBlock},
		{"unrecognized value fails conservative (block, matching not_a_pr)", review.PremiseState("bogus"), review.ShippableBlock},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := review.PremiseFloor(tc.state)
			if got != tc.want {
				t.Errorf("PremiseFloor(%q) = %s, want %s", tc.state, got, tc.want)
			}
		})
	}
}
