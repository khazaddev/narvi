package handoff

import (
	"fmt"
	"strings"

	"github.com/narvidev/narvi/internal/domain/reviewpost"
)

// RenderComment renders findings (already-built via BuildFindingInputs +
// reviewpost.BuildFinding -- the caller's own job, mirroring
// reviewpost.RenderVerdictComment's identical "typed fields in, markdown
// out, nothing parsed back" discipline) into the PR comment v1's own
// requirement posts (§14.4: "post the sentinel's summary as a PR
// comment"). Never called with an empty findings slice -- the caller
// (sessionactor's own handoffsentinel.go) already established there is
// something to report before ever reaching this function; a sentinel
// with nothing to say posts nothing at all (silence is correct).
func RenderComment(findings []reviewpost.Finding) string {
	var b strings.Builder

	b.WriteString("### Handoff readiness\n\n")
	b.WriteString("This pull request was opened from a scoped prototyping session. ")
	b.WriteString("The following item(s) likely need engineering follow-up before this ships:\n\n")

	for _, f := range findings {
		if f.Line != nil {
			fmt.Fprintf(&b, "- **%s** `%s:%d` -- %s\n", f.Severity, f.FilePath, *f.Line, f.Description)
		} else {
			fmt.Fprintf(&b, "- **%s** `%s` -- %s\n", f.Severity, f.FilePath, f.Description)
		}
	}

	b.WriteString("\n_Posted automatically by Narvi's handoff-readiness sentinel (§14.4) -- not a full code review._\n")

	return b.String()
}
