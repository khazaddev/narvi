package reviewpost_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/narvidev/narvi/internal/domain/review"
	"github.com/narvidev/narvi/internal/domain/reviewpost"
)

func TestRenderManifestComment_CleanManifest(t *testing.T) {
	t.Parallel()

	got := reviewpost.RenderManifestComment(nil, 5, false, false)

	for _, want := range []string{
		"Release manifest check",
		strconv.Itoa(5),
		"No compliance issues found",
		"not triggered",
		"release manifest check",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderManifestComment() missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Findings:") {
		t.Errorf("RenderManifestComment() with no findings should not render a Findings section:\n%s", got)
	}
	if strings.Contains(got, "PARTIAL") {
		t.Errorf("RenderManifestComment() with coveragePartial=false should not mention partial coverage:\n%s", got)
	}
}

// TestRenderManifestComment_PartialCoverageQualifiesCleanManifest proves
// audit-fix should-fix #5: an unqualified "No compliance issues found"
// claim is never posted when coverage was silently partial -- the
// rendered text must qualify the claim AND surface the partial-coverage
// note, rather than asserting a completeness guarantee ListMergedBetween
// never actually gave it.
func TestRenderManifestComment_PartialCoverageQualifiesCleanManifest(t *testing.T) {
	t.Parallel()

	got := reviewpost.RenderManifestComment(nil, 5, false, true)

	if strings.Contains(got, "No compliance issues found: every constituent PR") {
		t.Errorf("RenderManifestComment() with coveragePartial=true must NOT render the unqualified completeness claim:\n%s", got)
	}
	for _, want := range []string{
		"No compliance issues found among the 5 PR(s) checked",
		"PARTIAL",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderManifestComment() missing %q in:\n%s", want, got)
		}
	}
}

// TestRenderManifestComment_PartialCoverageStillSurfacesNoteWithFindings
// proves the partial-coverage note is surfaced even when findings is
// non-empty -- a maintainer reading real findings should still know more
// might exist beyond what was actually covered.
func TestRenderManifestComment_PartialCoverageStillSurfacesNoteWithFindings(t *testing.T) {
	t.Parallel()

	findings := []review.ManifestFinding{
		{Kind: review.ManifestFindingRedAtMerge, PRNumber: 1, PRTitle: "x"},
	}
	got := reviewpost.RenderManifestComment(findings, 5, false, true)

	if !strings.Contains(got, "PARTIAL") {
		t.Errorf("RenderManifestComment() with findings AND coveragePartial=true should still surface the partial-coverage note:\n%s", got)
	}
}

func TestRenderManifestComment_WithFindingsAndAggregateReview(t *testing.T) {
	t.Parallel()

	findings := []review.ManifestFinding{
		{Kind: review.ManifestFindingUnreviewedMerge, PRNumber: 142, PRTitle: "fix: thing", Detail: "admin override"},
		{Kind: review.ManifestFindingRedAtMerge, PRNumber: 156, PRTitle: "chore: other"},
		{Kind: review.ManifestFindingUnreviewedRevert, PRNumber: 160, PRTitle: "feat: risky", Detail: "2h"},
	}

	got := reviewpost.RenderManifestComment(findings, 12, true, false)

	for _, want := range []string{
		"Findings:",
		"#142",
		"admin override",
		"#156",
		"red (CI failing)",
		"#160",
		"reverted 2h after merge",
		"unreviewed",
		"aggregate diff review",
		strconv.Itoa(12),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderManifestComment() missing %q in:\n%s", want, got)
		}
	}
}

// TestRenderManifestComment_NeverMentionsRiskLevelOrShippable proves this
// rendering stays honest to §15.4: the manifest check never computes or
// renders a Shippable/PremiseState -- this is an audit, not a risk
// verdict.
func TestRenderManifestComment_NeverMentionsRiskLevelOrShippable(t *testing.T) {
	t.Parallel()

	got := reviewpost.RenderManifestComment([]review.ManifestFinding{
		{Kind: review.ManifestFindingUnreviewedMerge, PRNumber: 1, PRTitle: "x"},
	}, 3, false, false)

	for _, mustNotContain := range []string{"Shippable", "shippable", "Premise", "Risk:"} {
		if strings.Contains(got, mustNotContain) {
			t.Errorf("RenderManifestComment() unexpectedly contains %q -- this is an audit, never a risk verdict (§15.4):\n%s", mustNotContain, got)
		}
	}
}
