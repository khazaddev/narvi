package reviewpost

import (
	"fmt"
	"regexp"
	"strings"
)

// This file implements §26.2's own "graduated remediation"
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
//
// # Adversarial-review fix: idempotency + sanitization (items 3 and 4)
//
// This function was originally unconditional and unescaped: every call
// wrote proposedBody + a fresh <details> block wrapping originalBody
// VERBATIM, with no marker distinguishing "a real, human/other-authored
// original" from "Narvi's own previous rewrite", and no defense against
// proposedBody/originalBody content that could interact with GitHub's own
// markdown/keyword parser. Two independent problems followed directly
// from that:
//
//   - NOT IDEMPOTENT (item 3, HIGH): the outbox is at-least-once by
//     construction (no dedupe on CreateOutboxEntry, no per-PR claim row),
//     and re-review is the designed norm (§24 allows up to 10
//     automatic re-reviews per PR). A second delivery -- whether a
//     genuine second verdict proposing a new rewrite, or a plain retry of
//     the SAME delivery after a lost PATCH response -- re-fetches the
//     PR's CURRENT body via GetPRBody, which by then IS this function's
//     own previous output. Wrapping that in ANOTHER <details> block
//     labeled "Original description" replaces the real original with
//     Narvi's own prior rewrite, and duplicates the footer, compounding
//     on every further cycle.
//   - UNSANITIZED (item 4, MODERATE): proposedBody is model-authored free
//     text written verbatim onto a PR body -- unlike a comment, GitHub
//     parses CLOSING KEYWORDS ("Closes #N", "Fixes owner/repo#N") and
//     "@org/team" MENTIONS out of a PR's own description and acts on them
//     at merge time / immediately, a semantically-active surface no prior
//     model output in this codebase had ever reached verbatim before this
//     Step (contrast pushpr.go's own deterministic, server-composed PR
//     body text). Separately, originalBody was interpolated into the
//     <details> block unescaped -- an original containing its own literal
//     "</details>" could terminate the wrapper early, spilling arbitrary
//     content out of the "preserved" collapsed section.
//
// The fix for both, together: a fixed, machine-checkable marker
// (autofixMarkerLine) identifies THIS function's own prior output, so a
// re-render can detect it and re-extract the REAL original from within
// it (extractPreservedOriginal) instead of nesting a new wrapper around
// an already-rewritten body -- proven by TestRenderAutofixBody_
// DoubleRenderEqualsSingleRender (double-render == single-render, the
// review's own stated bar). originalBody is escaped (escapeFindingDescription,
// this package's own existing '<'/'>' escaper, rendercomment.go --
// reused rather than duplicated) BEFORE it is ever embedded, so a raw
// "</details>" can never appear inside the collapsed block, closing it
// early, AND so extraction can safely re-use the SAME escaped bytes
// verbatim on every subsequent round without re-escaping (which would
// otherwise double-encode on each cycle). proposedBody is defanged
// (defangClosingKeywords, defangMentions, below) before it is ever
// embedded, wrapping any closing-keyword-plus-reference sequence or
// @-mention in an inline code span -- GitHub does not parse closing
// keywords or mentions out of a code span, so this neutralizes the
// merge-time/notification side effect while leaving the proposed text
// otherwise fully readable.

// autofixMarkerLine is the fixed, first line of every body this function
// produces -- an HTML comment, invisible in GitHub's own rendered view,
// existing purely so a LATER call can recognize "this originalBody is
// already one of my own outputs" (extractPreservedOriginal below) rather
// than treating a re-review's freshly-fetched CURRENT body as a brand new,
// never-before-seen original. Versioned ("v1") so a future FORMAT change
// to this function can deliberately stop recognizing an old-format body as
// extractable (the safe default on a version mismatch: fall through to
// treating it as an ordinary, non-Narvi original -- see
// extractPreservedOriginal's own doc comment).
const autofixMarkerLine = "<!-- narvi:description-autofix:v1 -->"

// autofixDetailsOpen/autofixDetailsClose/autofixFooter are the fixed
// substrings this function's own output is built from -- named constants
// so RenderAutofixBody's own construction and extractPreservedOriginal's
// own parsing stay byte-for-byte in sync by construction (both read from
// the SAME constants), rather than two independently-hand-kept copies of
// the same literal text silently drifting apart.
const (
	autofixDetailsOpen  = "<details>\n<summary>Original description</summary>\n\n"
	autofixDetailsClose = "\n\n</details>"
	autofixFooter       = "_Description automatically updated by Narvi's server-side review tool (§26.2) -- the original is preserved above._"
)

// autofixOriginalBodyPlaceholder is what RenderAutofixBody renders inside
// the collapsed original-description block when originalBody is blank --
// a real, if uncommon, case (a PR opened with no description at all).
// Mirrors this package's own established "an honest placeholder, never a
// blank section" discipline (renderArchDecision's own blank-field
// handling, digest.go). Never itself passed through escapeFindingDescription
// (it contains no '<'/'>' to escape, and is server-authored constant text,
// never user content) -- extractPreservedOriginal re-uses it verbatim on a
// later round exactly like any other already-escaped original content.
const autofixOriginalBodyPlaceholder = "_(no original description)_"

// extractPreservedOriginal reports whether previousBody is ALREADY one of
// RenderAutofixBody's own outputs -- a re-review's freshly-fetched CURRENT
// PR body, when this repo's description-autofix flag already fired once
// before -- and, when it is, returns the REAL original description that
// output preserved, exactly as it was escaped and embedded at that
// earlier time (never re-escaped here -- see this file's own top doc
// comment for why re-using the same escaped bytes verbatim is what keeps
// repeated rounds idempotent instead of double-encoding).
//
// Anchored at BOTH ends of previousBody (the marker line at the very
// start, autofixFooter at the very end, both fixed constants this
// function alone controls) rather than a single forward substring search:
// a forward-only search for autofixDetailsOpen would find the FIRST
// occurrence in the string, which an adversarial proposedBody could spoof
// by embedding its OWN fake "<details><summary>Original description
// </summary>...</details>" sequence ahead of the real one this function
// appends -- silently and permanently displacing the real preserved
// original on every future round. Because RenderAutofixBody always
// appends its own real <details> block AFTER proposedBody, the REAL block
// is always the LAST one in the string it produces -- so this function
// anchors from the END (requiring the exact fixed footer as a suffix,
// then finding the LAST autofixDetailsOpen before it) rather than the
// START, closing that spoofing path structurally rather than by trusting
// proposedBody's own content not to attempt it.
//
// ok is false whenever previousBody does not match this function's own
// exact shape (a genuinely human/other-authored original, an
// old-format/differently-versioned marker, or any other mismatch) -- the
// safe default: RenderAutofixBody's own caller then falls through to
// treating previousBody as an ordinary, never-before-seen original,
// exactly like this function's pre-fix behavior. This can never produce a
// FALSE POSITIVE extraction (claiming content is preserved-original when
// it is not) without an exact match on marker+footer+well-formed details
// block, all three of which only this function itself ever produces
// together.
func extractPreservedOriginal(previousBody string) (preserved string, ok bool) {
	body := strings.TrimSpace(previousBody)
	if !strings.HasPrefix(body, autofixMarkerLine) {
		return "", false
	}
	if !strings.HasSuffix(body, autofixFooter) {
		return "", false
	}

	openIdx := strings.LastIndex(body, autofixDetailsOpen)
	if openIdx == -1 {
		return "", false
	}
	contentStart := openIdx + len(autofixDetailsOpen)

	closeIdx := strings.Index(body[contentStart:], autofixDetailsClose)
	if closeIdx == -1 {
		return "", false
	}
	contentEnd := contentStart + closeIdx

	return body[contentStart:contentEnd], true
}

// closingReferencePattern matches a single GitHub issue/PR reference in
// one of the three shapes GitHub's own "linking a pull request to an
// issue using a keyword" documentation names: a bare "#123", a cross-repo
// "owner/repo#123", or a full "https://github.com/owner/repo/issues/123"
// (or ".../pull/123") URL.
const closingReferencePattern = `(?:[\w.-]+/[\w.-]+)?#\d+|https?://github\.com/[\w.-]+/[\w.-]+/(?:issues|pull)/\d+`

// closingReferenceRe matches one closingReferencePattern occurrence,
// standalone -- used to defang each reference INSIDE an already-located
// keyword+reference-list match (defangClosingKeywords below), one at a
// time, so the keyword itself stays outside any code span while every
// reference after it is individually fenced.
var closingReferenceRe = regexp.MustCompile(`(?i)` + closingReferencePattern)

// closingKeywordSequenceRe matches one of GitHub's own nine documented
// PR-body closing keywords (close, closes, closed, fix, fixes, fixed,
// resolve, resolves, resolved) followed by one or more issue/PR
// references -- GitHub's own docs additionally support a single keyword
// closing SEVERAL issues at once ("Closes #10, #11, #12"), so the
// reference portion allows a ","/"and"/"&"-separated run rather than only
// a single reference.
var closingKeywordSequenceRe = regexp.MustCompile(
	`(?i)\b(close[sd]?|fix(?:e[sd])?|resolve[sd]?)\b((?:[\s:]*(?:` + closingReferencePattern + `)(?:\s*(?:,|&|and)\s*)?)+)`,
)

// defangClosingKeywords wraps every issue/PR reference following one of
// GitHub's own closing keywords in an inline code span -- GitHub does not
// parse closing keywords, or auto-link the references after them, out of
// a code span (https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues-and-pull-requests/linking-a-pull-request-to-an-issue),
// so this neutralizes the merge-time auto-close side effect while leaving
// the keyword itself, and the surrounding prose, fully readable and
// otherwise untouched. A pure string transform (§26.2 review's own
// framing) -- no I/O, no time.Now(), no randomness.
func defangClosingKeywords(s string) string {
	return closingKeywordSequenceRe.ReplaceAllStringFunc(s, func(match string) string {
		sub := closingKeywordSequenceRe.FindStringSubmatch(match)
		keyword, refs := sub[1], sub[2]
		return keyword + closingReferenceRe.ReplaceAllString(refs, "`$0`")
	})
}

// mentionRe matches a GitHub @-mention ("@username" or "@org/team"),
// capturing the character immediately BEFORE the "@" (or the empty string
// at the start of s) as its own group -- Go's RE2 engine (regexp) supports
// no lookbehind, so this is how defangMentions below avoids mis-matching
// an email address's own "@" (e.g. "foo@bar.com"): requiring the
// preceding character be neither a word character nor "@"/"/" excludes
// exactly that case while still matching a mention at the very start of a
// line or after whitespace/punctuation.
var mentionRe = regexp.MustCompile(`(^|[^\w@/])@[\w-]+(?:/[\w-]+)?`)

// defangMentions wraps every @-mention in s in an inline code span --
// GitHub does not send a notification for an @-mention inside a code
// span, so this neutralizes proposedBody's own ability to notify an
// arbitrary user/team the moment this body is written, the SAME
// "pure string transform, no I/O" discipline defangClosingKeywords above
// follows.
func defangMentions(s string) string {
	return mentionRe.ReplaceAllStringFunc(s, func(match string) string {
		at := strings.IndexByte(match, '@')
		return match[:at] + "`" + match[at:] + "`"
	})
}

// RenderAutofixBody composes the new PR body text §26.2's own graduated
// remediation writes to a Narvi-authored PR (behind the per-repo
// descriptionAutofix flag): proposedBody (the agent's own rewrite
// proposal, VerdictInput.Digest.ProposedBody, already non-blank by the
// time this is ever called) rendered first -- DEFANGED (defangClosingKeywords,
// defangMentions above) so its own closing-keyword/mention vocabulary
// cannot trigger GitHub's merge-time auto-close or a notification -- then
// originalBody (the PR's own CURRENT body, freshly re-fetched at delivery
// time -- NEVER the agent's own possibly-stale copy of it, since the
// agent's own view of title+body is untrusted input, §5.2, and may
// already be out of date by delivery time) preserved, ESCAPED
// (escapeFindingDescription, this package's own existing '<'/'>' escaper)
// so it cannot terminate the wrapper early, inside a collapsed <details>
// block -- §26.2's own explicit requirement: "preserving the original in
// a collapsed block". Pure (§11): no I/O, no time.Now(), no randomness --
// the caller supplies both strings already fetched/validated.
//
// IDEMPOTENT (adversarial-review fix, item 3): when originalBody is
// ALREADY one of this function's own previous outputs (extractPreservedOriginal
// above returns ok=true -- a re-review, or a retried delivery, re-fetching
// a body this SAME notifier already rewrote), the REAL preserved original
// is re-extracted and re-used verbatim, rather than nesting Narvi's own
// prior rewrite as though IT were the original -- see
// TestRenderAutofixBody_DoubleRenderEqualsSingleRender for the property
// this guarantees: double-render(original, p1, p2) == single-render(original,
// p2), the review's own stated bar for "idempotent".
//
// The TITLE is never part of this content, and never rewritten anywhere
// in this codebase (§26.2: "the title is never rewritten automatically,
// in either case") -- this function produces body text only; its one
// caller (internal/app/outboxworker's own description-autofix notifier)
// never touches the PR's title field at all.
func RenderAutofixBody(originalBody, proposedBody string) string {
	var preservedOriginal string
	if extracted, ok := extractPreservedOriginal(originalBody); ok {
		preservedOriginal = extracted
	} else {
		original := strings.TrimSpace(originalBody)
		if original == "" {
			preservedOriginal = autofixOriginalBodyPlaceholder
		} else {
			preservedOriginal = escapeFindingDescription(original)
		}
	}

	defangedProposed := defangMentions(defangClosingKeywords(strings.TrimSpace(proposedBody)))

	var b strings.Builder
	b.WriteString(autofixMarkerLine)
	b.WriteString("\n")
	b.WriteString(defangedProposed)
	b.WriteString("\n\n")
	b.WriteString(autofixDetailsOpen)
	b.WriteString(preservedOriginal)
	b.WriteString(autofixDetailsClose)
	fmt.Fprintf(&b, "\n\n%s\n", autofixFooter)
	return b.String()
}
