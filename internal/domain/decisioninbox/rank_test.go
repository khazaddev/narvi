package decisioninbox_test

import (
	"testing"
	"time"

	"github.com/narvidev/narvi/internal/domain/decisioninbox"
)

func TestSortIndex(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	keys := []decisioninbox.RankKey{
		{Kind: decisioninbox.KindNeedsAttention, EnteredQueueAt: base},                  // 0
		{Kind: decisioninbox.KindReadyToMerge, EnteredQueueAt: base.Add(2 * time.Hour)}, // 1: newer ready-to-merge
		{Kind: decisioninbox.KindReadyToMerge, EnteredQueueAt: base},                    // 2: older ready-to-merge -- must precede index 1
		{Kind: decisioninbox.KindNeedsReview, EnteredQueueAt: base},                     // 3
		{Kind: decisioninbox.KindAwaitingApproval, EnteredQueueAt: base},                // 4
	}

	got := decisioninbox.SortIndex(keys)
	want := []int{2, 1, 3, 4, 0}

	if len(got) != len(want) {
		t.Fatalf("SortIndex() len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SortIndex()[%d] = %d, want %d (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestSortIndex_Stable(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	// Three rows with IDENTICAL kind and instant -- order must be
	// preserved (sort.SliceStable), never permuted.
	keys := []decisioninbox.RankKey{
		{Kind: decisioninbox.KindNeedsReview, EnteredQueueAt: base},
		{Kind: decisioninbox.KindNeedsReview, EnteredQueueAt: base},
		{Kind: decisioninbox.KindNeedsReview, EnteredQueueAt: base},
	}
	got := decisioninbox.SortIndex(keys)
	want := []int{0, 1, 2}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SortIndex() = %v, want %v (stability violated)", got, want)
		}
	}
}

func TestLess(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		a, b decisioninbox.RankKey
		want bool
	}{
		{
			name: "lower cost kind sorts first regardless of age",
			a:    decisioninbox.RankKey{Kind: decisioninbox.KindReadyToMerge, EnteredQueueAt: base},
			b:    decisioninbox.RankKey{Kind: decisioninbox.KindNeedsAttention, EnteredQueueAt: base.Add(-1000 * time.Hour)},
			want: true,
		},
		{
			name: "same kind: older entered-queue sorts first",
			a:    decisioninbox.RankKey{Kind: decisioninbox.KindNeedsReview, EnteredQueueAt: base},
			b:    decisioninbox.RankKey{Kind: decisioninbox.KindNeedsReview, EnteredQueueAt: base.Add(time.Hour)},
			want: true,
		},
		{
			name: "same kind: newer entered-queue does not sort first",
			a:    decisioninbox.RankKey{Kind: decisioninbox.KindNeedsReview, EnteredQueueAt: base.Add(time.Hour)},
			b:    decisioninbox.RankKey{Kind: decisioninbox.KindNeedsReview, EnteredQueueAt: base},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := decisioninbox.Less(tc.a, tc.b); got != tc.want {
				t.Errorf("Less(%+v, %+v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
