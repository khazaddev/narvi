package reviewpost_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/domain/reviewpost"
)

// TestComputeFormalReviewEvent covers every (shippable, risk,
// blockOnHighRisk) combination that matters, including blockOnHighRisk
// both ON and OFF for every risk tier -- the explicit test-coverage item
// this Step's own brief names ("blockOnHighRisk both on and off").
func TestComputeFormalReviewEvent(t *testing.T) {
	tests := []struct {
		name            string
		shippable       review.Shippable
		risk            review.RiskLevel
		blockOnHighRisk bool
		want            reviewpost.FormalReviewEvent
	}{
		{
			name:            "shippable block, blockOnHighRisk off -- still REQUEST_CHANGES",
			shippable:       review.ShippableBlock,
			risk:            review.RiskLevelLow,
			blockOnHighRisk: false,
			want:            reviewpost.FormalReviewEventRequestChanges,
		},
		{
			name:            "shippable block, blockOnHighRisk on -- still REQUEST_CHANGES",
			shippable:       review.ShippableBlock,
			risk:            review.RiskLevelHigh,
			blockOnHighRisk: true,
			want:            reviewpost.FormalReviewEventRequestChanges,
		},
		{
			name:            "needs_human, low risk, blockOnHighRisk off -- COMMENT",
			shippable:       review.ShippableNeedsHuman,
			risk:            review.RiskLevelLow,
			blockOnHighRisk: false,
			want:            reviewpost.FormalReviewEventComment,
		},
		{
			name:            "needs_human, high risk, blockOnHighRisk OFF -- COMMENT (the off case)",
			shippable:       review.ShippableNeedsHuman,
			risk:            review.RiskLevelHigh,
			blockOnHighRisk: false,
			want:            reviewpost.FormalReviewEventComment,
		},
		{
			name:            "needs_human, high risk, blockOnHighRisk ON -- REQUEST_CHANGES (the on case)",
			shippable:       review.ShippableNeedsHuman,
			risk:            review.RiskLevelHigh,
			blockOnHighRisk: true,
			want:            reviewpost.FormalReviewEventRequestChanges,
		},
		{
			name:            "auto, high risk, blockOnHighRisk ON -- REQUEST_CHANGES even though shippable is auto",
			shippable:       review.ShippableAuto,
			risk:            review.RiskLevelHigh,
			blockOnHighRisk: true,
			want:            reviewpost.FormalReviewEventRequestChanges,
		},
		{
			name:            "auto, high risk, blockOnHighRisk off -- COMMENT",
			shippable:       review.ShippableAuto,
			risk:            review.RiskLevelHigh,
			blockOnHighRisk: false,
			want:            reviewpost.FormalReviewEventComment,
		},
		{
			name:            "auto, medium risk, blockOnHighRisk on -- COMMENT (medium is never affected by blockOnHighRisk)",
			shippable:       review.ShippableAuto,
			risk:            review.RiskLevelMedium,
			blockOnHighRisk: true,
			want:            reviewpost.FormalReviewEventComment,
		},
		{
			name:            "auto, low risk, blockOnHighRisk on -- COMMENT",
			shippable:       review.ShippableAuto,
			risk:            review.RiskLevelLow,
			blockOnHighRisk: true,
			want:            reviewpost.FormalReviewEventComment,
		},
		{
			name:            "defense in depth: unrecognized shippable fails conservative to REQUEST_CHANGES",
			shippable:       "bogus",
			risk:            review.RiskLevelLow,
			blockOnHighRisk: false,
			want:            reviewpost.FormalReviewEventRequestChanges,
		},
		{
			name:            "defense in depth: unrecognized risk + blockOnHighRisk on fails conservative to REQUEST_CHANGES",
			shippable:       review.ShippableAuto,
			risk:            "bogus",
			blockOnHighRisk: true,
			want:            reviewpost.FormalReviewEventRequestChanges,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := reviewpost.ComputeFormalReviewEvent(tc.shippable, tc.risk, tc.blockOnHighRisk)
			if got != tc.want {
				t.Errorf("ComputeFormalReviewEvent(%q, %q, %v) = %q, want %q", tc.shippable, tc.risk, tc.blockOnHighRisk, got, tc.want)
			}
		})
	}
}

// TestComputeFormalReviewEvent_NeverApproves proves this function never
// returns anything but COMMENT/REQUEST_CHANGES across a broad sweep of
// inputs -- APPROVE must never be producible here (Step 58's own future
// eligibility engine is the dedicated, later home for approving a PR).
func TestComputeFormalReviewEvent_NeverApproves(t *testing.T) {
	shippables := []review.Shippable{review.ShippableAuto, review.ShippableNeedsHuman, review.ShippableBlock}
	risks := []review.RiskLevel{review.RiskLevelLow, review.RiskLevelMedium, review.RiskLevelHigh}

	for _, s := range shippables {
		for _, r := range risks {
			for _, b := range []bool{true, false} {
				got := reviewpost.ComputeFormalReviewEvent(s, r, b)
				if got != reviewpost.FormalReviewEventComment && got != reviewpost.FormalReviewEventRequestChanges {
					t.Errorf("ComputeFormalReviewEvent(%q, %q, %v) = %q, want COMMENT or REQUEST_CHANGES only", s, r, b, got)
				}
			}
		}
	}
}
