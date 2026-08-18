package reviewpost_test

import (
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/domain/reviewpost"
)

// digestInjectionPayload is the SAME "<details>...</details>" / "List<int>"
// shaped injection concretely named by the Phase 5 audit's MEDIUM finding:
// an unclosed "<details>" in model-authored digest text would be rendered
// as raw HTML by GitHub's markdown engine, swallowing the Architecture/
// Risks sections that follow it -- hiding exactly the merge-decision
// content §26.1 front-loads this readout around, and it is also the §16
// inbox headline. "List<int>" is the SAME hazard's simpler, more common
// form: any generic/comparison a model plausibly writes about the diff it
// is reviewing.
const digestInjectionPayload = "<details><summary>evil</summary>swallowed content</details> uses List<int> here"

// digestInjectionEscaped is what escapeFindingDescription (rendercomment.go)
// must turn digestInjectionPayload into.
const digestInjectionEscaped = "&lt;details&gt;&lt;summary&gt;evil&lt;/summary&gt;swallowed content&lt;/details&gt; uses List&lt;int&gt; here"

// TestRenderVerdictComment_UntrustedDigestFieldsAreEscaped is the Phase 5
// audit's MEDIUM-finding regression test: table-driven over every
// model-authored free-text Digest field the audit found rendered
// UNESCAPED (Steps 66/67/69's own additions, never brought in line with
// f.Description's pre-existing escaping discipline) -- proving each one
// now neutralizes an injected "<details>"/"</details>"/"List<int>" rather
// than emitting it raw into the rendered comment, matching Description's
// own pre-existing TestRenderVerdictComment_FindingDescriptionEscapesAngleBrackets
// treatment (rendercomment_test.go).
func TestRenderVerdictComment_UntrustedDigestFieldsAreEscaped(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		digest reviewpost.Digest
	}{
		{
			name:   "Digest.Summary",
			digest: reviewpost.Digest{Summary: digestInjectionPayload},
		},
		{
			name:   "Digest.AdequacyExplanation",
			digest: reviewpost.Digest{Summary: "No changes of note.", AdequacyExplanation: digestInjectionPayload},
		},
		{
			name:   "Digest.StackRisks",
			digest: reviewpost.Digest{Summary: "No changes of note.", StackRisks: digestInjectionPayload},
		},
		{
			name:   "Digest.UnverifiedLimits",
			digest: reviewpost.Digest{Summary: "No changes of note.", UnverifiedLimits: digestInjectionPayload},
		},
		{
			name: "ArchDecision.Decision",
			digest: reviewpost.Digest{Summary: "No changes of note.", ArchDecisions: []reviewpost.ArchDecision{
				{Decision: digestInjectionPayload, RejectedAlternative: "alt", ConventionConformance: "conforms"},
			}},
		},
		{
			name: "ArchDecision.RejectedAlternative",
			digest: reviewpost.Digest{Summary: "No changes of note.", ArchDecisions: []reviewpost.ArchDecision{
				{Decision: "decision", RejectedAlternative: digestInjectionPayload, ConventionConformance: "conforms"},
			}},
		},
		{
			name: "ArchDecision.ConventionConformance",
			digest: reviewpost.Digest{Summary: "No changes of note.", ArchDecisions: []reviewpost.ArchDecision{
				{Decision: "decision", RejectedAlternative: "alt", ConventionConformance: digestInjectionPayload},
			}},
		},
		{
			name:   "Digest.ProposedBody",
			digest: reviewpost.Digest{Summary: "No changes of note.", ProposedBody: digestInjectionPayload},
		},
		{
			name:   "Digest.ContestedPoints",
			digest: reviewpost.Digest{Summary: "No changes of note.", ContestedPoints: digestInjectionPayload},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := reviewpost.RenderVerdictComment(baseVerdict(), nil, tt.digest, "Summary.", "narvi-bot", reviewpost.LabelLowRisk)

			if strings.Contains(got, "<details><summary>evil</summary>") || strings.Contains(got, "List<int>") {
				t.Errorf("RenderVerdictComment() rendered an UNESCAPED injection from %s -- want '<'/'>' escaped, got:\n%s", tt.name, got)
			}
			if !strings.Contains(got, digestInjectionEscaped) {
				t.Errorf("RenderVerdictComment() missing the escaped %s content, want to contain %q, got:\n%s", tt.name, digestInjectionEscaped, got)
			}
		})
	}
}

// TestRenderVerdictComment_FindingFilePathEscapesBacktickAndNewline is the
// Phase 5 audit's own explicitly-named FilePath case: FilePath is
// interpolated raw inside a SINGLE-backtick inline code span
// ("`%s:%d`") -- a backtick inside it closes that code span early
// (GitHub's markdown parser matches the FIRST subsequent backtick), and a
// newline breaks the finding's own one-line bullet onto new markdown
// line(s) -- either lets an attacker-controlled path splice
// attacker-chosen markdown/HTML into the surrounding comment structure.
func TestRenderVerdictComment_FindingFilePathEscapesBacktickAndNewline(t *testing.T) {
	t.Parallel()

	findings := []reviewpost.Finding{
		{Severity: review.RiskLevelMedium, FilePath: "a`b\nc\rd", Description: "an ordinary finding", StartLine: 5, EndLine: 5},
	}
	digest := reviewpost.Digest{Summary: "No changes of note."}

	got := reviewpost.RenderVerdictComment(baseVerdict(), findings, digest, "Summary.", "narvi-bot", reviewpost.LabelLowRisk)

	if strings.Contains(got, "a`b") || strings.Contains(got, "b\nc") || strings.Contains(got, "c\rd") {
		t.Errorf("RenderVerdictComment() rendered an unescaped backtick/newline/carriage-return from Finding.FilePath, breaking out of its code span:\n%s", got)
	}
	want := "`a'b c d:5`"
	if !strings.Contains(got, want) {
		t.Errorf("RenderVerdictComment() missing the escaped FilePath inside its own code span, want %q in:\n%s", want, got)
	}
}

// TestRenderVerdictComment_FindingFilePathEscapedInEveryLineShape covers
// the OTHER two FilePath-in-backticks rendering shapes
// (range-anchored "`%s:%d-%d`" and unanchored "`%s`") --
// TestRenderVerdictComment_FindingFilePathEscapesBacktickAndNewline above
// only exercises the single-line-anchored shape.
func TestRenderVerdictComment_FindingFilePathEscapedInEveryLineShape(t *testing.T) {
	t.Parallel()

	findings := []reviewpost.Finding{
		{Severity: review.RiskLevelMedium, FilePath: "range`path", Description: "range-anchored", StartLine: 10, EndLine: 12},
		{Severity: review.RiskLevelMedium, FilePath: "unanchored`path", Description: "unanchored"},
	}
	digest := reviewpost.Digest{Summary: "No changes of note."}

	got := reviewpost.RenderVerdictComment(baseVerdict(), findings, digest, "Summary.", "narvi-bot", reviewpost.LabelLowRisk)

	if strings.Contains(got, "range`path") || strings.Contains(got, "unanchored`path") {
		t.Errorf("RenderVerdictComment() rendered an unescaped backtick from FilePath in the range-anchored or unanchored shape:\n%s", got)
	}
	for _, want := range []string{"`range'path:10-12`", "`unanchored'path`"} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderVerdictComment() missing %q in:\n%s", want, got)
		}
	}
}
