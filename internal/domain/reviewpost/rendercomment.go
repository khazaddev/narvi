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
//  1. Header -- risk badge (Risk/Premise), why-line (summary, the SAME
//     pre-existing free-text parameter this function has always taken,
//     rendered in the SAME position immediately under the header
//     bullets), and shippable class (Shippable). §26.1 item 1 names
//     exactly these three; TestsCoverage/DocsDrift/FilesChanged/
//     BlastRadius, previously also rendered as flat bullets here, MOVE
//     out of the header in Step 66 -- items 4 and 5 below name new, more
//     specific homes for each of them. §26.2/Step 67 adds ONE further
//     header bullet -- "Description adequacy" (digest.DescriptionAdequacy
//     + digest.AdequacyExplanation) -- immediately after Premise: the
//     SAME structural role Premise already plays (a closed-vocabulary
//     assessment that floors Shippable, §26.2's own third raise-only
//     floor), so it belongs beside it, not buried in a later section.
//  2. "What this PR does" -- digest.Summary, verbatim. §26.2/Step 67
//     additionally renders a "Suggested PR description" block here, when
//     digest.ProposedBody is non-blank -- see renderProposedBody's own
//     doc comment below for why this renders for EVERY PR regardless of
//     authorship, unconditionally on ProposedBody alone.
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
// # Step 69 (§26.4): "Contested points"
//
// digest.ContestedPoints (digest.go, the deep path's own inter-agent
// disagreement narrative -- populated by counter-review synthesis, empty
// on every light-path review since there is no counter-reviewer there to
// disagree with anything) renders as its own "### Contested points"
// section, immediately after "Risks to the stack" and before the
// collapsed appendix -- visible without expanding anything, matching
// §26.4's own "agent disagreement is precisely the signal that a human
// must decide" framing. Rendered ONLY when non-blank, with no "none
// reported" fallback (unlike Architecture choices/Risks to the stack
// above) -- see renderContestedPoints' own doc comment for why, the SAME
// reasoning renderProposedBody already established for ProposedBody.
//
// TestsCoverage/DocsDrift/FilesChanged/BlastRadius are v's own PRE-
// EXISTING fields (review.Verdict, unchanged by this Step -- Step 66 adds
// no new field to that closed, seven-field type, digest.go's own doc
// comment) -- this function only ever changes WHERE they render, never
// what they are or how internal/app/reviewverdict.Insert persists them.
func RenderVerdictComment(v review.Verdict, findings []Finding, digest Digest, summary, botHandle, syncedLabel string) string {
	var b strings.Builder

	// --- 1. Header (§26.1 item 1, §26.2 item 1) -- risk badge, why-line,
	// shippable class, PLUS §26.2/Step 67's own "Description adequacy"
	// bullet (immediately after Premise -- the same structural role: a
	// closed-vocabulary assessment that floors Shippable).
	b.WriteString("### Code review verdict\n\n")
	fmt.Fprintf(&b, "- **Risk**: %s\n", v.RiskLevel)
	fmt.Fprintf(&b, "- **Premise**: %s\n", v.Premise)
	fmt.Fprintf(&b, "- **Description adequacy**: %s -- %s\n", digest.DescriptionAdequacy, strings.TrimSpace(digest.AdequacyExplanation))
	fmt.Fprintf(&b, "- **Shippable**: %s (server-computed)\n\n", v.Shippable)

	b.WriteString(strings.TrimSpace(summary))
	b.WriteString("\n\n")

	// --- 2. "What this PR does" (§26.1 item 2).
	b.WriteString("### What this PR does\n\n")
	b.WriteString(strings.TrimSpace(digest.Summary))
	b.WriteString("\n\n")

	// --- §26.2/Step 67: "Suggested PR description", when the agent
	// proposed one -- see renderProposedBody's own doc comment for why
	// this renders unconditionally on ProposedBody alone, for every PR
	// regardless of authorship (graduated remediation, §26.2, decides
	// only whether a REAL WRITE also happens, never whether this
	// rendered suggestion is visible).
	b.WriteString(renderProposedBody(digest.ProposedBody))

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
		fmt.Fprintf(&b, "- **Stack risks**: %s\n", stackRisks)
	}
	if unverifiedLimits != "" {
		fmt.Fprintf(&b, "- **Not verified**: %s\n", unverifiedLimits)
	}
	if len(v.BlastRadius) == 0 && stackRisks == "" && unverifiedLimits == "" {
		b.WriteString("_No stack risks reported for this review._\n")
	}
	b.WriteString("\n")

	// --- Step 69 (§26.4): "Contested points" -- rendered only when the
	// deep path's counter-review synthesis actually produced one; see
	// renderContestedPoints' own doc comment for why an empty value
	// renders no section at all.
	b.WriteString(renderContestedPoints(digest.ContestedPoints))

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
			description := escapeFindingDescription(f.Description)
			switch {
			case f.StartLine != 0 && f.StartLine == f.EndLine:
				fmt.Fprintf(&b, "- [%s/%s] `%s:%d`: %s\n", kind, f.Severity, f.FilePath, f.StartLine, description)
			case f.StartLine != 0:
				fmt.Fprintf(&b, "- [%s/%s] `%s:%d-%d`: %s\n", kind, f.Severity, f.FilePath, f.StartLine, f.EndLine, description)
			default:
				fmt.Fprintf(&b, "- [%s/%s] `%s`: %s\n", kind, f.Severity, f.FilePath, description)
			}
		}
	}
	b.WriteString("\n</details>\n\n")

	fmt.Fprintf(&b, "_Posted via Narvi's server-side verdict tool_ · labels synced: `%s`\n\n", syncedLabel)
	b.WriteString(RerunGuidance(botHandle))

	return b.String()
}

// renderProposedBody renders proposedBody (digest.ProposedBody, the
// agent's own optional PR-body rewrite proposal, §26.2/Step 67) as a
// collapsed "Suggested PR description" block -- an empty/blank
// proposedBody renders NOTHING at all (not even a "none reported"
// sentence, unlike Architecture choices/Risks to the stack above): most
// reviews propose no rewrite at all (only a review that itself found the
// description drifting or misleading has any reason to), so an empty
// section header on every ordinary review would be pure noise, unlike
// those other two sections, which are meant to appear on every review
// with SOMETHING to say about them.
//
// Rendered UNCONDITIONALLY on authorship or the repo's own
// descriptionAutofix flag -- §26.2's own graduated remediation is about
// which PRs additionally get a REAL WRITE (internal/app/outboxworker's
// own description-autofix notifier, re-verifying Narvi-authorship and the
// flag SERVER-SIDE at delivery time, never here): this function has no
// access to either fact (a pure function, §11, no I/O of its own) and
// would have no principled way to gate on them even if it did -- a human
// reading this comment benefits from seeing the suggestion either way,
// exactly like §26.2's own "human-authored PRs: a proposed body rendered
// in the digest" wording already states explicitly for that case, applied
// here uniformly to every case rather than conditionally.
func renderProposedBody(proposedBody string) string {
	trimmed := strings.TrimSpace(proposedBody)
	if trimmed == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("<details>\n<summary>Suggested PR description</summary>\n\n")
	b.WriteString(trimmed)
	b.WriteString("\n\n</details>\n\n")
	return b.String()
}

// renderContestedPoints renders contestedPoints (digest.ContestedPoints, the
// deep path's own inter-agent disagreement narrative, §26.4/Step 69) as a
// "### Contested points" section -- an empty/blank contestedPoints renders
// NOTHING at all (not even a "none reported" sentence, the SAME choice
// renderProposedBody already makes for ProposedBody immediately below, for
// the identical reason): the ordinary, common case -- every light-path
// review, and most deep-path reviews too -- has no disagreement to report
// at all, so a "none reported" heading on every ordinary review would be
// pure noise, unlike Architecture choices/Risks to the stack above, which
// are meant to appear on every review with something to say (even if that
// something is an honest "none reported").
//
// Unlike renderProposedBody's own collapsed `<details>` treatment,
// Contested points renders as a plain, always-visible section: §26.4's own
// "agent disagreement is precisely the signal that a human must decide"
// framing is exactly the kind of content a maintainer should not have to
// expand a fold to see.
func renderContestedPoints(contestedPoints string) string {
	trimmed := strings.TrimSpace(contestedPoints)
	if trimmed == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("### Contested points\n\n")
	b.WriteString(trimmed)
	b.WriteString("\n\n")
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

// findingDescriptionEscaper escapes '<' and '>' in a Finding's own
// Description before it is interpolated into the rendered comment body.
// Description is model-authored free text (finding.go's own doc comment)
// and can legitimately contain generics, tags, or comparisons (e.g.
// "List<int>", "a < b") -- left unescaped, GitHub's own markdown renderer
// would read an unescaped '<' as the start of a literal HTML tag rather
// than the model's own text, silently dropping or mangling it. Narrower
// than html.EscapeString on purpose: '<'/'>' are the only characters that
// can change how GitHub parses the SURROUNDING markdown structure here;
// '&' (which html.EscapeString would also escape) has no equivalent
// structural effect in this context, so leaving it alone avoids turning
// ordinary prose like "fetch & retry" into "fetch &amp; retry" for no
// safety benefit.
var findingDescriptionEscaper = strings.NewReplacer("<", "&lt;", ">", "&gt;")

// escapeFindingDescription applies findingDescriptionEscaper to s -- the
// one call site every Description interpolation in this file goes
// through, so the escaping discipline can never be forgotten at a new
// call site the way an inline strings.NewReplacer call at each site
// could be.
func escapeFindingDescription(s string) string {
	return findingDescriptionEscaper.Replace(s)
}
