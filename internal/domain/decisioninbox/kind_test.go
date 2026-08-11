package decisioninbox_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/decisioninbox"
)

func TestDecisionCost_Ordering(t *testing.T) {
	t.Parallel()

	// §16.1: ready_to_merge first (cheapest), then needs_review, then
	// awaiting_approval, then needs_attention -- a strict ascending chain.
	costs := make([]int, len(decisioninbox.AllKinds))
	for i, k := range decisioninbox.AllKinds {
		costs[i] = decisioninbox.DecisionCost(k)
	}
	for i := 1; i < len(costs); i++ {
		if costs[i-1] >= costs[i] {
			t.Errorf("DecisionCost(%s)=%d not < DecisionCost(%s)=%d", decisioninbox.AllKinds[i-1], costs[i-1], decisioninbox.AllKinds[i], costs[i])
		}
	}
}

func TestDecisionCost_UnrecognizedFailsConservative(t *testing.T) {
	t.Parallel()

	worst := decisioninbox.DecisionCost(decisioninbox.KindNeedsAttention)
	got := decisioninbox.DecisionCost(decisioninbox.Kind("bogus"))
	if got <= worst {
		t.Errorf("DecisionCost(bogus) = %d, want > %d (worse than every real Kind)", got, worst)
	}
}

func TestAllKinds_CoversExactlyFour(t *testing.T) {
	t.Parallel()

	want := map[decisioninbox.Kind]bool{
		decisioninbox.KindReadyToMerge:     true,
		decisioninbox.KindNeedsReview:      true,
		decisioninbox.KindAwaitingApproval: true,
		decisioninbox.KindNeedsAttention:   true,
	}
	if len(decisioninbox.AllKinds) != len(want) {
		t.Fatalf("AllKinds has %d entries, want %d", len(decisioninbox.AllKinds), len(want))
	}
	for _, k := range decisioninbox.AllKinds {
		if !want[k] {
			t.Errorf("AllKinds contains unexpected Kind %q", k)
		}
	}
}
