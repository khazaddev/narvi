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
// Step) -- rendered inside the collapsed appendix below, EXACTLY the same
// "typed fields -> rendered text, never parsed back" discipline as every
// other field this function renders (review/doc.go's own top-level
// stance): a finding's own IdentityHash is deliberately NOT rendered here
// (an internal reconciliation key, not something a PR reader needs to
// see) -- RenderAlreadyAnsweredFacts (reconcile.go) is the one place a
// short form of it is ever surfaced, for a different, internal audience
// (a re-reviewing agent's own prompt, not a human reading the PR).
//
// # Step 66 (§26.1): restructured into a merge readout
//
// digest (digest.go, VerdictInput.Digest, already validated -- Digest.
// Summary non-empty, per ValidateVerdictInput) supplies the new content
// this Step inserts BETWEEN the header and the appendix. The rendering
// below follows §26.1's own five-part shape (that section's own numbered
// list) exactly:
//
//  1. Header, UNCHANGED (§26.1 item 1's own words: "do not change this
//     part") -- risk badge (Risk/Premise), why-line (summary, the SAME
//     pre-existing free-text parameter this function has always taken,
//     rendered in the SAME position immediately under the header
//     bullets), and shippable class (Shippable). §26.1 item 1 names
//     exactly these three; TestsCoverage/DocsDrift/FilesChanged/
//     BlastRadius, previously also rendered as flat bullets here, MOVE
//     out of the header in this Step -- items 4 and 5 below name new,
//     more specific homes for each of them.
//  2. "What this PR does" -- digest.Summary, verbatim.
//  3. "Architecture choices" -- one block per digest.ArchDecisions
//     element (renderArchDecision below); an honest "none reported"
//     sentence when empty, so the readout's own section skeleton stays
//     the same shape across every verdict rather than a heading
//     appearing and disappearing based on what one particular review
//     happened to report.
//  4. "Risks to the stack" -- v.BlastRadius (v's own pre-existing,
//     server-typed fixed vocabulary, MOVED here from the old flat bullet
//     list -- §26.1 item 4 names it as part of THIS section) plus
//     digest.StackRisks (coupling/deployment risks, reversibility) and
//     digest.UnverifiedLimits (honest "not verified" limits); the same
//     "none reported" fallback as item 3 when all three are
//     empty/blank.
//  5. Collapsed appendix (a GitHub-native `<details>` block -- rendered
//     unconditionally, so TestsCoverage/DocsDrift/FilesChanged are always
//     visible on expand even for a verdict reporting zero findings) --
//     TestsCoverage/DocsDrift/FilesChanged (MOVED here from the old flat
//     bullet list) and Findings (present only when non-empty), RETAINED
//     INTACT -- exactly the same per-finding rendering this function has
//     always produced, §26.1's own words: "demoted to supporting
//     evidence", never restructured or dropped.
//
// TestsCoverage/DocsDrift/FilesChanged/BlastRadius are v's own PRE-
// EXISTING fields (review.Verdict, unchanged by this Step -- Step 66 adds
// no new field to that closed, seven-field type, digest.go's own doc
// comment) -- this function only ever changes WHERE they render, never
// what they are or how internal/app/reviewverdict.Insert persists them.
func RenderVerdictComment(v review.Verdict, findings []Finding, digest Digest, summary, botHandle, syncedLabel string) string {
	var b strings.Builder

	// --- 1. Header (§26.1 item 1) -- risk badge, why-line, shippable
	// class. UNCHANGED in shape from this function's own pre-Step-66
	// rendering, minus the four fields that move to items 4/5 below.
	b.WriteString("### Code review verdict\n\n")
	fmt.Fprintf(&b, "- **Risk**: %s\n", v.RiskLevel)
	fmt.Fprintf(&b, "- **Premise**: %s\n", v.Premise)
	fmt.Fprintf(&b, "- **Shippable**: %s (server-computed)\n\n", v.Shippable)

	b.WriteString(strings.TrimSpace(summary))
	b.WriteString("\n\n")

	// --- 2. "What this PR does" (§26.1 item 2).
	b.WriteString("### What this PR does\n\n")
	b.WriteString(strings.TrimSpace(digest.Summary))
	b.WriteString("\n\n")

	// --- 3. "Architecture choices" (§26.1 item 3).
	b.WriteString("### Architecture choices\n\n")
	if len(digest.ArchDecisions) > 0 {
		for _, ad := range digest.ArchDecisions {
			b.WriteString(renderArchDecision(ad))
		}
	} else {
		b.WriteString("_No architecture decisions reported for this review._\n")
	}
	b.WriteString("\n")

	// --- 4. "Risks to the stack" (§26.1 item 4).
	b.WriteString("### Risks to the stack\n\n")
	stackRisks := strings.TrimSpace(digest.StackRisks)
	unverifiedLimits := strings.TrimSpace(digest.UnverifiedLimits)
	if len(v.BlastRadius) > 0 {
		tags := make([]string, len(v.BlastRadius))
		for i, t := range v.BlastRadius {
			tags[i] = string(t)
		}
		fmt.Fprintf(&b, "- **Blast radius**: %s\n", strings.Join(tags, ", "))
	}
	if stackRisks != "" {
		b.WriteString(stackRisks)
		b.WriteString("\n")
	}
	if unverifiedLimits != "" {
		fmt.Fprintf(&b, "- **Not verified**: %s\n", unverifiedLimits)
	}
	if len(v.BlastRadius) == 0 && stackRisks == "" && unverifiedLimits == "" {
		b.WriteString("_No stack risks reported for this review._\n")
	}
	b.WriteString("\n")

	// --- 5. Collapsed appendix (§26.1 item 5) -- findings, coverage,
	// docs-drift, files changed: retained intact, demoted to supporting
	// evidence. Rendered UNCONDITIONALLY (never gated on len(findings) >
	// 0) so TestsCoverage/DocsDrift/FilesChanged are always available on
	// expand, even for a verdict reporting zero findings -- GitHub renders
	// markdown inside <details>/<summary> only when a blank line separates
	// the </summary> tag from the content that follows it.
	b.WriteString("<details>\n<summary>Findings, coverage &amp; docs-drift (supporting evidence)</summary>\n\n")
	fmt.Fprintf(&b, "- **Test coverage**: %s\n", v.TestsCoverage)
	fmt.Fprintf(&b, "- **Docs drift**: %s\n", v.DocsDrift)
	fmt.Fprintf(&b, "- **Files changed**: %d\n", v.FilesChanged)

	if len(findings) > 0 {
		b.WriteString("\n**Findings:**\n\n")
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
	}
	b.WriteString("\n</details>\n\n")

	fmt.Fprintf(&b, "_Posted via Narvi's server-side verdict tool_ · labels synced: `%s`\n\n", syncedLabel)
	b.WriteString(RerunGuidance(botHandle))

	return b.String()
}

// renderArchDecision renders one ArchDecision as a single bullet block --
// three labeled sub-lines under one leading bullet, mirroring this
// file's own established "typed struct -> a few labeled lines" rendering
// idiom (e.g. the header's own "- **Risk**: ..." lines above). A blank
// field (legal -- ArchDecision is not validation-enforced this Step,
// digest.go's own doc comment) still renders its own labeled line with
// nothing after it, rather than silently omitting that sub-line: an
// agent that names a Decision but leaves RejectedAlternative blank is
// visibly incomplete to a human reader, never silently smoothed over.
func renderArchDecision(ad ArchDecision) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- **Decision**: %s\n", strings.TrimSpace(ad.Decision))
	fmt.Fprintf(&b, "  **Alternative rejected**: %s\n", strings.TrimSpace(ad.RejectedAlternative))
	fmt.Fprintf(&b, "  **Convention conformance**: %s\n", strings.TrimSpace(ad.ConventionConformance))
	return b.String()
}
