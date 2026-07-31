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
func RenderVerdictComment(v review.Verdict, summary, botHandle, syncedLabel string) string {
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

	fmt.Fprintf(&b, "_Posted via Narvi's server-side verdict tool_ · labels synced: `%s`\n\n", syncedLabel)
	b.WriteString(RerunGuidance(botHandle))

	return b.String()
}
