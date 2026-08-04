package review_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/review"
)

func int64Ptr(n int64) *int64 { return &n }

// TestComputeReleaseManifestFindings_UnreviewedMerge covers §15.2's own
// first example finding: a PR merging without an approving review, with
// and without a confirmed admin override.
func TestComputeReleaseManifestFindings_UnreviewedMerge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		pr             review.MergedPR
		wantFindings   int
		wantDetail     string
		wantNoFindings bool
	}{
		{
			name: "no approving review, confirmed admin override",
			pr: review.MergedPR{
				Number: 142, Title: "fix: thing",
				HasApprovingReview:     false,
				MergedViaAdminOverride: true,
			},
			wantFindings: 1,
			wantDetail:   "admin override",
		},
		{
			name: "no approving review, override not confirmed",
			pr: review.MergedPR{
				Number: 142, Title: "fix: thing",
				HasApprovingReview:     false,
				MergedViaAdminOverride: false,
			},
			wantFindings: 1,
			wantDetail:   "",
		},
		{
			name: "has an approving review: no finding",
			pr: review.MergedPR{
				Number: 143, Title: "fix: other thing",
				HasApprovingReview: true,
			},
			wantNoFindings: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			findings := review.ComputeReleaseManifestFindings([]review.MergedPR{tc.pr})
			if tc.wantNoFindings {
				if len(findings) != 0 {
					t.Fatalf("got %d findings, want 0: %+v", len(findings), findings)
				}
				return
			}
			if len(findings) != tc.wantFindings {
				t.Fatalf("got %d findings, want %d: %+v", len(findings), tc.wantFindings, findings)
			}
			f := findings[0]
			if f.Kind != review.ManifestFindingUnreviewedMerge {
				t.Errorf("Kind = %s, want %s", f.Kind, review.ManifestFindingUnreviewedMerge)
			}
			if f.PRNumber != tc.pr.Number {
				t.Errorf("PRNumber = %d, want %d", f.PRNumber, tc.pr.Number)
			}
			if f.Detail != tc.wantDetail {
				t.Errorf("Detail = %q, want %q", f.Detail, tc.wantDetail)
			}
		})
	}
}

// TestComputeReleaseManifestFindings_RedAtMerge proves ONLY a positively
// confirmed CIConclusionFailure ever produces a finding -- CIConclusionUnknown
// and the zero value are DELIBERATELY not treated as red (manifestcheck.go's
// own doc comment on this reasoned departure from the package's usual
// fail-conservative policy).
func TestComputeReleaseManifestFindings_RedAtMerge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		conclusion   review.CIConclusion
		wantFindings int
	}{
		{"failure: a finding", review.CIConclusionFailure, 1},
		{"success: no finding", review.CIConclusionSuccess, 0},
		{"unknown: no finding (not asserted without evidence)", review.CIConclusionUnknown, 0},
		{"zero value: no finding (never treated as failure)", review.CIConclusion(""), 0},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pr := review.MergedPR{
				Number: 156, Title: "chore: thing",
				HasApprovingReview:     true,
				CIConclusionAtMergeSHA: tc.conclusion,
			}
			findings := review.ComputeReleaseManifestFindings([]review.MergedPR{pr})
			if len(findings) != tc.wantFindings {
				t.Fatalf("got %d findings, want %d: %+v", len(findings), tc.wantFindings, findings)
			}
			if tc.wantFindings == 1 && findings[0].Kind != review.ManifestFindingRedAtMerge {
				t.Errorf("Kind = %s, want %s", findings[0].Kind, review.ManifestFindingRedAtMerge)
			}
		})
	}
}

// TestComputeReleaseManifestFindings_UnreviewedRevert covers §15.2's own
// third example finding, including the revert-timing detail string.
func TestComputeReleaseManifestFindings_UnreviewedRevert(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		pr           review.MergedPR
		wantFindings int
		wantDetail   string
	}{
		{
			name: "reverted, revert unreviewed, timing known (2h)",
			pr: review.MergedPR{
				Number: 160, Title: "feat: risky thing",
				HasApprovingReview:        true,
				WasReverted:               true,
				RevertReviewed:            false,
				RevertedAfterMergeSeconds: int64Ptr(2 * 3600),
			},
			wantFindings: 1,
			wantDetail:   "2h",
		},
		{
			name: "reverted, revert unreviewed, timing unknown",
			pr: review.MergedPR{
				Number: 160, Title: "feat: risky thing",
				HasApprovingReview: true,
				WasReverted:        true,
				RevertReviewed:     false,
			},
			wantFindings: 1,
			wantDetail:   "",
		},
		{
			name: "reverted, revert WAS reviewed: no finding",
			pr: review.MergedPR{
				Number: 161, Title: "feat: another",
				HasApprovingReview: true,
				WasReverted:        true,
				RevertReviewed:     true,
			},
			wantFindings: 0,
		},
		{
			name: "never reverted: no finding",
			pr: review.MergedPR{
				Number: 162, Title: "feat: yet another",
				HasApprovingReview: true,
				WasReverted:        false,
			},
			wantFindings: 0,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			findings := review.ComputeReleaseManifestFindings([]review.MergedPR{tc.pr})
			if len(findings) != tc.wantFindings {
				t.Fatalf("got %d findings, want %d: %+v", len(findings), tc.wantFindings, findings)
			}
			if tc.wantFindings == 0 {
				return
			}
			f := findings[0]
			if f.Kind != review.ManifestFindingUnreviewedRevert {
				t.Errorf("Kind = %s, want %s", f.Kind, review.ManifestFindingUnreviewedRevert)
			}
			if f.Detail != tc.wantDetail {
				t.Errorf("Detail = %q, want %q", f.Detail, tc.wantDetail)
			}
		})
	}
}

// TestComputeReleaseManifestFindings_MultipleFindingsPerPR proves a
// single PR can contribute all three finding kinds at once, and that
// findings across multiple PRs preserve input order.
func TestComputeReleaseManifestFindings_MultipleFindingsPerPR(t *testing.T) {
	t.Parallel()

	merged := []review.MergedPR{
		{
			Number: 1, Title: "a",
			HasApprovingReview:     false,
			CIConclusionAtMergeSHA: review.CIConclusionFailure,
			WasReverted:            true,
			RevertReviewed:         false,
		},
		{
			Number: 2, Title: "b",
			HasApprovingReview: true,
		},
	}

	findings := review.ComputeReleaseManifestFindings(merged)
	if len(findings) != 3 {
		t.Fatalf("got %d findings, want 3: %+v", len(findings), findings)
	}
	wantKinds := []review.ManifestFindingKind{
		review.ManifestFindingUnreviewedMerge,
		review.ManifestFindingRedAtMerge,
		review.ManifestFindingUnreviewedRevert,
	}
	for i, want := range wantKinds {
		if findings[i].Kind != want {
			t.Errorf("findings[%d].Kind = %s, want %s", i, findings[i].Kind, want)
		}
		if findings[i].PRNumber != 1 {
			t.Errorf("findings[%d].PRNumber = %d, want 1", i, findings[i].PRNumber)
		}
	}
}

// TestComputeReleaseManifestFindings_EmptyInput proves a clean/empty
// manifest produces zero findings, never a nil-vs-empty panic or a
// spurious finding.
func TestComputeReleaseManifestFindings_EmptyInput(t *testing.T) {
	t.Parallel()
	if got := review.ComputeReleaseManifestFindings(nil); len(got) != 0 {
		t.Errorf("ComputeReleaseManifestFindings(nil) = %+v, want empty", got)
	}
	if got := review.ComputeReleaseManifestFindings([]review.MergedPR{}); len(got) != 0 {
		t.Errorf("ComputeReleaseManifestFindings([]) = %+v, want empty", got)
	}
}
