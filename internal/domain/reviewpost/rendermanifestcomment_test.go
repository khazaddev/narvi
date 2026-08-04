package reviewpost_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/domain/reviewpost"
)

func TestRenderManifestComment_CleanManifest(t *testing.T) {
	t.Parallel()

	got := reviewpost.RenderManifestComment(nil, 5, false)

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
}

func TestRenderManifestComment_WithFindingsAndAggregateReview(t *testing.T) {
	t.Parallel()

	findings := []review.ManifestFinding{
		{Kind: review.ManifestFindingUnreviewedMerge, PRNumber: 142, PRTitle: "fix: thing", Detail: "admin override"},
		{Kind: review.ManifestFindingRedAtMerge, PRNumber: 156, PRTitle: "chore: other"},
		{Kind: review.ManifestFindingUnreviewedRevert, PRNumber: 160, PRTitle: "feat: risky", Detail: "2h"},
	}

	got := reviewpost.RenderManifestComment(findings, 12, true)

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
	}, 3, false)

	for _, mustNotContain := range []string{"Shippable", "shippable", "Premise", "Risk:"} {
		if strings.Contains(got, mustNotContain) {
			t.Errorf("RenderManifestComment() unexpectedly contains %q -- this is an audit, never a risk verdict (§15.4):\n%s", mustNotContain, got)
		}
	}
}
