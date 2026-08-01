package reviewpost_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/domain/reviewpost"
)

func TestRenderVerdictComment(t *testing.T) {
	v := review.Verdict{
		RiskLevel:         review.RiskLevelMedium,
		Premise:           review.PremiseStateOK,
		BlastRadius:       []review.Tag{review.TagAuth, review.TagContracts},
		FilesChanged:      7,
		TestsCoverage:     review.TestsCoverageStateAdequate,
		DocsDrift:         review.DocsDriftStateNone,
		ProposedShippable: review.ProposedShippableNeedsHuman,
		Shippable:         review.ShippableNeedsHuman,
	}

	got := reviewpost.RenderVerdictComment(v, nil, "Timing-unsafe comparison in verify.go.", "narvi-bot", reviewpost.LabelMediumRisk)

	for _, want := range []string{
		string(review.RiskLevelMedium),
		string(review.PremiseStateOK),
		string(review.TestsCoverageStateAdequate),
		string(review.DocsDriftStateNone),
		strconv.Itoa(v.FilesChanged),
		string(review.TagAuth),
		string(review.TagContracts),
		string(review.ShippableNeedsHuman),
		"Timing-unsafe comparison in verify.go.",
		reviewpost.LabelMediumRisk,
		"server-side verdict tool",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderVerdictComment() missing %q in:\n%s", want, got)
		}
	}

	// The rendered comment must ALWAYS carry the rerun guidance, so a
	// review a human wants to re-run always has an actionable next step.
	if !strings.Contains(got, reviewpost.RerunGuidance("narvi-bot")) {
		t.Errorf("RenderVerdictComment() missing RerunGuidance in:\n%s", got)
	}
}

// TestRenderVerdictComment_EmptyBlastRadiusOmitsLine proves an empty
// BlastRadius (a legitimate value, review.Verdict's own doc comment) never
// renders a dangling "Blast radius:" line with nothing after it.
func TestRenderVerdictComment_EmptyBlastRadiusOmitsLine(t *testing.T) {
	v := review.Verdict{
		RiskLevel:     review.RiskLevelLow,
		Premise:       review.PremiseStateOK,
		TestsCoverage: review.TestsCoverageStateAdequate,
		DocsDrift:     review.DocsDriftStateNone,
		Shippable:     review.ShippableAuto,
	}

	got := reviewpost.RenderVerdictComment(v, nil, "Nothing to flag.", "narvi-bot", reviewpost.LabelLowRisk)
	if strings.Contains(got, "Blast radius") {
		t.Errorf("RenderVerdictComment() rendered a Blast radius line for an empty BlastRadius:\n%s", got)
	}
}
