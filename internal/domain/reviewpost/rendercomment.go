package reviewpost

import (
	"fmt"
	"strings"

	"github.com/khazaddev/narvi/internal/domain/review"
)

// RenderVerdictComment renders v's own typed fields (the ONLY source of
// truth -- nothing here is ever parsed back out of this rendering
// afterward, review/doc.go's own "nothing here even imports a markdown
// parser, on principle" stance) into the markdown body the verdict-
// posting tool submits, either as an issue comment or as a formal
// review's own body (the SAME rendering either way -- ComputeFormalReviewEvent,
// formalreview.go, decides only which SUBMISSION MECHANISM carries this
// text, never a different text for each). summary is the agent's own
// free-text narrative (VerdictInput.Summary, already validated non-empty
// by ValidateVerdictInput); syncedLabel is the review:*-risk label
// ComputeLabelSync (label.go) just applied, rendered here purely for
// visibility (mirrors docs/design/mockups.html's own verdict-foot "labels
// synced: `review:medium-risk`" line); botHandle feeds RerunGuidance.
// findings (Step 48, additive) is v's own already-built []Finding (nil/
// empty for a verdict reporting none -- every verdict posted before this
// Step) -- rendered as bullets under the summary, EXACTLY the same
// "typed fields -> rendered text, never parsed back" discipline as every
// other field this function renders (review/doc.go's own top-level
// stance): a finding's own IdentityHash is deliberately NOT rendered here
// (an internal reconciliation key, not something a PR reader needs to
// see) -- RenderAlreadyAnsweredFacts (reconcile.go) is the one place a
// short form of it is ever surfaced, for a different, internal audience
// (a re-reviewing agent's own prompt, not a human reading the PR).
func RenderVerdictComment(v review.Verdict, findings []Finding, summary, botHandle, syncedLabel string) string {
	var b strings.Builder

	b.WriteString("### Code review verdict\n\n")
	fmt.Fprintf(&b, "- **Risk**: %s\n", v.RiskLevel)
	fmt.Fprintf(&b, "- **Premise**: %s\n", v.Premise)
	fmt.Fprintf(&b, "- **Test coverage**: %s\n", v.TestsCoverage)
	fmt.Fprintf(&b, "- **Docs drift**: %s\n", v.DocsDrift)
	fmt.Fprintf(&b, "- **Files changed**: %d\n", v.FilesChanged)
	if len(v.BlastRadius) > 0 {
		tags := make([]string, len(v.BlastRadius))
		for i, t := range v.BlastRadius {
			tags[i] = string(t)
		}
		fmt.Fprintf(&b, "- **Blast radius**: %s\n", strings.Join(tags, ", "))
	}
	fmt.Fprintf(&b, "- **Shippable**: %s (server-computed)\n\n", v.Shippable)

	b.WriteString(strings.TrimSpace(summary))
	b.WriteString("\n\n")

	if len(findings) > 0 {
		b.WriteString("**Findings:**\n\n")
		for _, f := range findings {
			kind := findingIdentityGeneralKind
			if f.SentinelKind != nil {
				kind = string(*f.SentinelKind)
			}
			// §22.1.1: StartLine/EndLine (server-computed, content-anchored
			// -- position.go) are the ONLY position ever rendered here once
			// they exist. f.Line (the model's own self-reported, UNVERIFIED
			// pointer) is deliberately never used as a rendering fallback
			// when StartLine is 0 (unanchored): rendering it anyway would
			// hand a maintainer exactly the "plausible-looking wrong
			// answer" §22.1.1 says is worse than no position at all --
			// StartLine==0 renders as no line reference whatsoever, an
			// honest "position not found", never a guess dressed up as a
			// real one.
			switch {
			case f.StartLine != 0 && f.StartLine == f.EndLine:
				fmt.Fprintf(&b, "- [%s/%s] `%s:%d`: %s\n", kind, f.Severity, f.FilePath, f.StartLine, f.Description)
			case f.StartLine != 0:
				fmt.Fprintf(&b, "- [%s/%s] `%s:%d-%d`: %s\n", kind, f.Severity, f.FilePath, f.StartLine, f.EndLine, f.Description)
			default:
				fmt.Fprintf(&b, "- [%s/%s] `%s`: %s\n", kind, f.Severity, f.FilePath, f.Description)
			}
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "_Posted via Narvi's server-side verdict tool_ · labels synced: `%s`\n\n", syncedLabel)
	b.WriteString(RerunGuidance(botHandle))

	return b.String()
}
