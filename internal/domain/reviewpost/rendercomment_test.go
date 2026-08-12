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

// TestRenderVerdictComment_AnchoredFindingRendersStartEndLine proves an
// anchored finding (§22.1.1) renders using StartLine/EndLine, never the
// model's own self-reported Line.
func TestRenderVerdictComment_AnchoredFindingRendersStartEndLine(t *testing.T) {
	v := review.Verdict{
		RiskLevel: review.RiskLevelLow, Premise: review.PremiseStateOK,
		TestsCoverage: review.TestsCoverageStateAdequate, DocsDrift: review.DocsDriftStateNone,
		Shippable: review.ShippableAuto,
	}
	modelLine := 999
	finding := reviewpost.Finding{
		Severity: review.RiskLevelMedium, FilePath: "main.go", Description: "off-by-one",
		Line:      &modelLine,
		StartLine: 10, EndLine: 12,
	}

	got := reviewpost.RenderVerdictComment(v, []reviewpost.Finding{finding}, "Summary.", "narvi-bot", reviewpost.LabelLowRisk)

	if !strings.Contains(got, "main.go:10-12") {
		t.Errorf("RenderVerdictComment() missing anchored range %q in:\n%s", "main.go:10-12", got)
	}
	if strings.Contains(got, "main.go:999") {
		t.Errorf("RenderVerdictComment() rendered the model's own unverified Line (999) instead of the anchored StartLine/EndLine:\n%s", got)
	}
}

// TestRenderVerdictComment_AnchoredSingleLineFindingOmitsRange proves a
// single-line anchor (StartLine == EndLine) renders as "file:N", never
// "file:N-N".
func TestRenderVerdictComment_AnchoredSingleLineFindingOmitsRange(t *testing.T) {
	v := review.Verdict{
		RiskLevel: review.RiskLevelLow, Premise: review.PremiseStateOK,
		TestsCoverage: review.TestsCoverageStateAdequate, DocsDrift: review.DocsDriftStateNone,
		Shippable: review.ShippableAuto,
	}
	finding := reviewpost.Finding{
		Severity: review.RiskLevelMedium, FilePath: "main.go", Description: "off-by-one",
		StartLine: 10, EndLine: 10,
	}

	got := reviewpost.RenderVerdictComment(v, []reviewpost.Finding{finding}, "Summary.", "narvi-bot", reviewpost.LabelLowRisk)

	if !strings.Contains(got, "main.go:10`") {
		t.Errorf("RenderVerdictComment() missing single-line anchor %q in:\n%s", "main.go:10`", got)
	}
	if strings.Contains(got, "main.go:10-10") {
		t.Errorf("RenderVerdictComment() rendered a redundant range (10-10) for a single-line anchor:\n%s", got)
	}
}

// TestRenderVerdictComment_UnanchoredFindingNeverRendersAGuessedLine is
// this Step's own central proof for the rendering side of §22.1.1: an
// unanchored finding (StartLine == 0) must NEVER render ANY line
// reference at all -- not the anchored range (there is none), and NOT
// the model's own self-reported Line either (that would be exactly the
// "plausible-looking wrong answer" §22.1.1 says is worse than nothing).
func TestRenderVerdictComment_UnanchoredFindingNeverRendersAGuessedLine(t *testing.T) {
	v := review.Verdict{
		RiskLevel: review.RiskLevelLow, Premise: review.PremiseStateOK,
		TestsCoverage: review.TestsCoverageStateAdequate, DocsDrift: review.DocsDriftStateNone,
		Shippable: review.ShippableAuto,
	}
	modelLine := 42
	finding := reviewpost.Finding{
		Severity: review.RiskLevelMedium, FilePath: "main.go", Description: "some finding",
		Line:      &modelLine, // the model's own self-report -- must be ignored
		StartLine: 0, EndLine: 0,
	}

	got := reviewpost.RenderVerdictComment(v, []reviewpost.Finding{finding}, "Summary.", "narvi-bot", reviewpost.LabelLowRisk)

	if strings.Contains(got, "main.go:42") {
		t.Errorf("RenderVerdictComment() rendered the model's own unverified Line (42) for an UNANCHORED finding -- must render no line at all:\n%s", got)
	}
	if !strings.Contains(got, "`main.go`") {
		t.Errorf("RenderVerdictComment() should still render the bare file path for an unanchored finding:\n%s", got)
	}
}
