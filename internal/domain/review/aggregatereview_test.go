package review_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/review"
)

// TestShouldRunAggregateReview_PathPrefixOverlap covers §15.3's own first
// OR-condition: "≥3 constituent PRs touch overlapping path prefixes".
func TestShouldRunAggregateReview_PathPrefixOverlap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		prs  []review.MergedPR
		want bool
	}{
		{
			name: "3 distinct PRs share one prefix: triggers",
			prs: []review.MergedPR{
				{Number: 1, ChangedPathPrefixes: []string{"internal/domain"}},
				{Number: 2, ChangedPathPrefixes: []string{"internal/domain"}},
				{Number: 3, ChangedPathPrefixes: []string{"internal/domain"}},
			},
			want: true,
		},
		{
			name: "only 2 PRs share a prefix: does not trigger",
			prs: []review.MergedPR{
				{Number: 1, ChangedPathPrefixes: []string{"internal/domain"}},
				{Number: 2, ChangedPathPrefixes: []string{"internal/domain"}},
			},
			want: false,
		},
		{
			name: "3 PRs, but each touches a DIFFERENT prefix: does not trigger",
			prs: []review.MergedPR{
				{Number: 1, ChangedPathPrefixes: []string{"internal/domain"}},
				{Number: 2, ChangedPathPrefixes: []string{"internal/app"}},
				{Number: 3, ChangedPathPrefixes: []string{"docs"}},
			},
			want: false,
		},
		{
			name: "a PR touching the same prefix twice counts once, not twice",
			prs: []review.MergedPR{
				{Number: 1, ChangedPathPrefixes: []string{"internal/domain", "internal/domain"}},
				{Number: 2, ChangedPathPrefixes: []string{"internal/domain"}},
			},
			// Still only 2 distinct PRs behind the prefix -- must not
			// trigger just because PR 1 listed it twice.
			want: false,
		},
		{
			name: "3 PRs share a prefix among several OTHER, non-overlapping ones",
			prs: []review.MergedPR{
				{Number: 1, ChangedPathPrefixes: []string{"internal/domain", "docs"}},
				{Number: 2, ChangedPathPrefixes: []string{"internal/domain", "internal/app"}},
				{Number: 3, ChangedPathPrefixes: []string{"internal/domain"}},
			},
			want: true,
		},
		{
			name: "empty prefixes never overlap",
			prs: []review.MergedPR{
				{Number: 1}, {Number: 2}, {Number: 3},
			},
			want: false,
		},
		{
			name: "no PRs at all",
			prs:  nil,
			want: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := review.ShouldRunAggregateReview(tc.prs); got != tc.want {
				t.Errorf("ShouldRunAggregateReview() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestShouldRunAggregateReview_HighRiskFlagged covers §15.3's own second
// OR-condition: "any constituent PR was flagged high-risk/critical by
// the team's own PR-tiering".
func TestShouldRunAggregateReview_HighRiskFlagged(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		prs  []review.MergedPR
		want bool
	}{
		{
			name: "one PR flagged high-risk among several clean ones: triggers",
			prs: []review.MergedPR{
				{Number: 1},
				{Number: 2, HighRiskFlagged: true},
			},
			want: true,
		},
		{
			name: "no PR flagged: does not trigger",
			prs: []review.MergedPR{
				{Number: 1}, {Number: 2},
			},
			want: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := review.ShouldRunAggregateReview(tc.prs); got != tc.want {
				t.Errorf("ShouldRunAggregateReview() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestShouldRunAggregateReview_ManualConflictResolution covers §15.3's
// own third OR-condition: "any constituent PR's merge required manually
// resolving a conflict".
func TestShouldRunAggregateReview_ManualConflictResolution(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		prs  []review.MergedPR
		want bool
	}{
		{
			name: "one PR required manual conflict resolution: triggers",
			prs: []review.MergedPR{
				{Number: 1},
				{Number: 2, HadManualConflictResolution: true},
			},
			want: true,
		},
		{
			name: "no PR required it: does not trigger",
			prs: []review.MergedPR{
				{Number: 1}, {Number: 2},
			},
			want: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := review.ShouldRunAggregateReview(tc.prs); got != tc.want {
				t.Errorf("ShouldRunAggregateReview() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestShouldRunAggregateReview_IsAnOR proves the three conditions
// genuinely OR together -- any single one firing is sufficient regardless
// of the other two being clean, and all three clean means no trigger.
func TestShouldRunAggregateReview_IsAnOR(t *testing.T) {
	t.Parallel()

	clean := []review.MergedPR{
		{Number: 1, ChangedPathPrefixes: []string{"a"}},
		{Number: 2, ChangedPathPrefixes: []string{"b"}},
	}
	if got := review.ShouldRunAggregateReview(clean); got {
		t.Errorf("all three conditions clean: ShouldRunAggregateReview() = true, want false")
	}

	withHighRisk := append(append([]review.MergedPR{}, clean...), review.MergedPR{Number: 3, HighRiskFlagged: true})
	if got := review.ShouldRunAggregateReview(withHighRisk); !got {
		t.Errorf("only high-risk flagged: ShouldRunAggregateReview() = false, want true")
	}

	withConflict := append(append([]review.MergedPR{}, clean...), review.MergedPR{Number: 3, HadManualConflictResolution: true})
	if got := review.ShouldRunAggregateReview(withConflict); !got {
		t.Errorf("only manual conflict resolution: ShouldRunAggregateReview() = false, want true")
	}
}
