package reviewverdict_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/reviewpost"
	"github.com/khazaddev/narvi/internal/domain/reviewverdict"
)

func TestFindingOutcomes_EmptyIsNotYetComputed(t *testing.T) {
	t.Parallel()

	outcomes, ok := reviewverdict.FindingOutcomes(nil)
	if ok {
		t.Fatalf("FindingOutcomes(nil) ok = true, want false (not yet computed)")
	}
	if outcomes != nil {
		t.Fatalf("FindingOutcomes(nil) outcomes = %v, want nil", outcomes)
	}
}

func TestFindingOutcomes_CountsAndOrdersDeterministically(t *testing.T) {
	t.Parallel()

	statuses := []reviewpost.FindingStatus{
		reviewpost.FindingStatusOpen,
		reviewpost.FindingStatusOpen,
		reviewpost.FindingStatusRebutted,
		reviewpost.FindingStatusFixApplied,
	}

	outcomes, ok := reviewverdict.FindingOutcomes(statuses)
	if !ok {
		t.Fatalf("FindingOutcomes(statuses) ok = false, want true")
	}
	if len(outcomes) != 3 {
		t.Fatalf("FindingOutcomes(statuses) = %d statuses, want 3", len(outcomes))
	}

	want := []reviewverdict.FindingStatusCount{
		{Status: reviewpost.FindingStatusOpen, Count: 2},
		{Status: reviewpost.FindingStatusFixApplied, Count: 1},
		{Status: reviewpost.FindingStatusRebutted, Count: 1},
	}
	for i, w := range want {
		if outcomes[i] != w {
			t.Errorf("outcomes[%d] = %+v, want %+v", i, outcomes[i], w)
		}
	}
}
