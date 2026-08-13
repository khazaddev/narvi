package reviewpost

import (
	"fmt"
	"strings"
)

// This file implements §26.2/Step 67's own "graduated remediation"
// content: the ACTUAL new PR body text a Narvi-authored PR's description
// gets rewritten to, when the per-repo descriptionAutofix flag is on
// (internal/app/outboxworker's own description-autofix notifier is the
// ONE caller, at delivery time, after both the Narvi-authorship and flag
// checks have already passed server-side -- see that notifier's own doc
// comment). Distinct from renderProposedBody (rendercomment.go), which
// renders the SAME proposedBody text as a read-only SUGGESTION inside the
// posted review comment, for every PR regardless of authorship --
// RenderAutofixBody below produces the text that becomes the pull
// request's own body field, only ever reached for the narrower,
// server-verified Narvi-authored+flag-on case.

// autofixOriginalBodyPlaceholder is what RenderAutofixBody renders inside
// the collapsed original-description block when originalBody is blank --
// a real, if uncommon, case (a PR opened with no description at all).
// Mirrors this package's own established "an honest placeholder, never a
// blank section" discipline (renderArchDecision's own blank-field
// handling, digest.go).
const autofixOriginalBodyPlaceholder = "_(no original description)_"

// RenderAutofixBody composes the new PR body text §26.2's own graduated
// remediation writes to a Narvi-authored PR (behind the per-repo
// descriptionAutofix flag): proposedBody (the agent's own rewrite
// proposal, VerdictInput.Digest.ProposedBody, already non-blank by the
// time this is ever called) rendered first, followed by originalBody (the
// PR's own CURRENT body, freshly re-fetched at delivery time -- NEVER the
// agent's own possibly-stale copy of it, since the agent's own view of
// title+body is untrusted input, §5.2, and may already be out of date by
// delivery time) preserved verbatim inside a collapsed <details> block --
// §26.2's own explicit requirement: "preserving the original in a
// collapsed block". Pure (§11): no I/O, no time.Now(), no randomness --
// the caller supplies both strings already fetched/validated.
//
// The TITLE is never part of this content, and never rewritten anywhere
// in this codebase (§26.2: "the title is never rewritten automatically,
// in either case") -- this function produces body text only; its one
// caller (internal/app/outboxworker's own description-autofix notifier)
// never touches the PR's title field at all.
func RenderAutofixBody(originalBody, proposedBody string) string {
	original := strings.TrimSpace(originalBody)
	if original == "" {
		original = autofixOriginalBodyPlaceholder
	}

	var b strings.Builder
	b.WriteString(strings.TrimSpace(proposedBody))
	b.WriteString("\n\n<details>\n<summary>Original description</summary>\n\n")
	b.WriteString(original)
	fmt.Fprintf(&b, "\n\n</details>\n\n_Description automatically updated by Narvi's server-side review tool (§26.2) -- the original is preserved above._\n")
	return b.String()
}
