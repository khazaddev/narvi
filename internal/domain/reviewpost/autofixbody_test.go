package reviewpost_test

import (
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/reviewpost"
)

// TestRenderAutofixBody_ProposedBeforeCollapsedOriginal proves §26.2's own
// central shape: the proposed body renders FIRST, visible without
// expanding anything, and the original renders SECOND, inside a collapsed
// <details> block -- "preserving the original in a collapsed block".
func TestRenderAutofixBody_ProposedBeforeCollapsedOriginal(t *testing.T) {
	t.Parallel()

	original := "Fixes a typo."
	proposed := "Rewrites the auth token refresh path to retry on transient network failures."

	got := reviewpost.RenderAutofixBody(original, proposed)

	if !strings.Contains(got, proposed) {
		t.Errorf("RenderAutofixBody() missing the proposed body in:\n%s", got)
	}
	if !strings.Contains(got, original) {
		t.Errorf("RenderAutofixBody() missing the original body in:\n%s", got)
	}

	proposedIdx := strings.Index(got, proposed)
	detailsIdx := strings.Index(got, "<details>")
	originalIdx := strings.Index(got, original)
	closeIdx := strings.Index(got, "</details>")
	if proposedIdx == -1 || detailsIdx == -1 || originalIdx == -1 || closeIdx == -1 {
		t.Fatalf("expected all four markers present, got:\n%s", got)
	}
	if proposedIdx >= detailsIdx || detailsIdx >= originalIdx || originalIdx >= closeIdx {
		t.Errorf("expected order [proposed, <details>, original, </details>], got indices %d, %d, %d, %d in:\n%s",
			proposedIdx, detailsIdx, originalIdx, closeIdx, got)
	}
	if !strings.Contains(got, "Original description") {
		t.Errorf("RenderAutofixBody() missing an \"Original description\" summary label in:\n%s", got)
	}
}

// TestRenderAutofixBody_BlankOriginalRendersPlaceholder proves a PR opened
// with no description at all (a real, if uncommon, case) still renders a
// valid, non-empty collapsed block -- never a dangling empty <details>
// section.
func TestRenderAutofixBody_BlankOriginalRendersPlaceholder(t *testing.T) {
	t.Parallel()

	got := reviewpost.RenderAutofixBody("", "A real proposed description.")

	if !strings.Contains(got, "no original description") {
		t.Errorf("RenderAutofixBody() missing an honest placeholder for a blank original body in:\n%s", got)
	}
}

// TestRenderAutofixBody_WhitespaceOnlyOriginalRendersPlaceholder proves
// the SAME placeholder fires for a whitespace-only original body, not
// only a literally-empty one.
func TestRenderAutofixBody_WhitespaceOnlyOriginalRendersPlaceholder(t *testing.T) {
	t.Parallel()

	got := reviewpost.RenderAutofixBody("   \n\t  ", "A real proposed description.")

	if !strings.Contains(got, "no original description") {
		t.Errorf("RenderAutofixBody() missing an honest placeholder for a whitespace-only original body in:\n%s", got)
	}
}

// TestRenderAutofixBody_NeverMentionsTitle documents §26.2's own explicit
// "the title is never rewritten automatically" rule at this function's
// own level: nothing in its own output vocabulary references a title at
// all -- this function's signature itself has no title parameter, so
// there is structurally nothing for a caller to even pass one through.
func TestRenderAutofixBody_NeverMentionsTitle(t *testing.T) {
	t.Parallel()

	got := reviewpost.RenderAutofixBody("Original.", "Proposed.")
	if strings.Contains(strings.ToLower(got), "title") {
		t.Errorf("RenderAutofixBody() output unexpectedly mentions \"title\" -- this function must never touch or reference the PR title:\n%s", got)
	}
}

// TestRenderAutofixBody_DoubleRenderEqualsSingleRender is item 3's own
// central regression test (adversarial-review fix, HIGH: "the autofix
// write is not idempotent"), pinned exactly at the bar the review itself
// stated: "double-render equals single-render". Simulates the real
// at-least-once delivery/re-review sequence -- render once (as if a first
// verdict's own delivery just wrote the PR body), then render AGAIN
// feeding the FIRST render's own output back in as originalBody (exactly
// what a re-fetched GetPRBody would return on a second delivery of the
// SAME payload, a plain outbox retry) -- and requires the two outputs be
// byte-for-byte IDENTICAL. Before this fix, the second render would have
// wrapped the first render's own output as though it were the original,
// growing the body and replacing the real original with Narvi's own
// prior rewrite.
func TestRenderAutofixBody_DoubleRenderEqualsSingleRender(t *testing.T) {
	t.Parallel()

	original := "Fixes a typo in the README."
	proposed := "Rewrites the auth token refresh path to retry on transient network failures."

	first := reviewpost.RenderAutofixBody(original, proposed)
	second := reviewpost.RenderAutofixBody(first, proposed)

	if first != second {
		t.Errorf("double-render != single-render:\nfirst:\n%s\n\nsecond:\n%s", first, second)
	}
	if !strings.Contains(second, original) {
		t.Errorf("second render lost the REAL original description, got:\n%s", second)
	}
}

// TestRenderAutofixBody_SecondReviewNewProposalPreservesRealOriginal
// proves the OTHER real trigger for a second delivery (not a retry of the
// SAME payload, but a genuinely NEW verdict from a later re-review
// proposing a DIFFERENT rewrite -- Step 65 allows up to 10 automatic
// re-reviews per PR): the SECOND render's own preserved-original block
// must still contain the REAL, human-authored original -- never the
// FIRST render's own proposed text -- even though the proposed text
// itself legitimately changes between rounds.
func TestRenderAutofixBody_SecondReviewNewProposalPreservesRealOriginal(t *testing.T) {
	t.Parallel()

	original := "Fixes a typo in the README."
	proposed1 := "First rewrite: fixes the retry loop."
	proposed2 := "Second rewrite, from a later re-review: also handles the edge case."

	first := reviewpost.RenderAutofixBody(original, proposed1)
	second := reviewpost.RenderAutofixBody(first, proposed2)

	if !strings.Contains(second, proposed2) {
		t.Errorf("second render missing the NEW proposed text, got:\n%s", second)
	}
	if !strings.Contains(second, original) {
		t.Errorf("second render lost the REAL original description, got:\n%s", second)
	}
	if strings.Contains(second, proposed1) {
		t.Errorf("second render's preserved-original block leaked the FIRST render's own proposed text -- the original must never be replaced by Narvi's own prior rewrite, got:\n%s", second)
	}
}

// TestRenderAutofixBody_TripleRenderStaysIdempotent extends the double-
// render property across a THIRD round (mirroring §24's own "up to
// 10 automatic re-reviews" reality -- this is never bounded at exactly
// two rounds in production) -- proves the fix converges, rather than
// merely surviving one extra round before some other compounding
// resurfaces.
func TestRenderAutofixBody_TripleRenderStaysIdempotent(t *testing.T) {
	t.Parallel()

	original := "Fixes a typo in the README."
	proposed := "Rewrites the auth token refresh path."

	first := reviewpost.RenderAutofixBody(original, proposed)
	second := reviewpost.RenderAutofixBody(first, proposed)
	third := reviewpost.RenderAutofixBody(second, proposed)

	if second != third {
		t.Errorf("third render diverged from the second -- idempotency did not converge:\nsecond:\n%s\n\nthird:\n%s", second, third)
	}
}

// TestRenderAutofixBody_ProposedBodySpoofingFakeMarkerNeverHijacksExtraction
// is the spoofing-resistance regression test the idempotency fix's own
// anchoring (extractPreservedOriginal, autofixbody.go) exists to close: a
// hostile proposedBody that embeds ITS OWN fake marker/details/footer
// sequence -- attempting to trick a LATER render into extracting the
// attacker's own injected text as though it were the real preserved
// original -- must never succeed. Because RenderAutofixBody always
// appends its own REAL <details> block strictly after proposedBody, the
// real block is always the last one in the string; a forward-only search
// would have been fooled by this exact input.
func TestRenderAutofixBody_ProposedBodySpoofingFakeMarkerNeverHijacksExtraction(t *testing.T) {
	t.Parallel()

	realOriginal := "The real, human-authored original description."
	hostileProposed := "Ignore the above.\n\n" +
		"<!-- narvi:description-autofix:v1 -->\n" +
		"INJECTED proposed text\n\n" +
		"<details>\n<summary>Original description</summary>\n\n" +
		"FORGED original -- this is an attack, not the real original\n\n" +
		"</details>\n\n" +
		"_Description automatically updated by Narvi's server-side review tool (§26.2) -- the original is preserved above._"

	first := reviewpost.RenderAutofixBody(realOriginal, hostileProposed)
	second := reviewpost.RenderAutofixBody(first, "A legitimate second-round proposal.")

	if strings.Contains(second, "FORGED original") {
		t.Errorf("second render extracted the ATTACKER's forged original instead of the real one, got:\n%s", second)
	}
	if !strings.Contains(second, realOriginal) {
		t.Errorf("second render lost the REAL original description to a spoofing attempt, got:\n%s", second)
	}
}

// TestRenderAutofixBody_OriginalContainingDetailsCloseCannotEscapeWrapper
// is item 4's own second bullet, reproduced then fixed: an originalBody
// containing its own literal "</details>" must not be able to terminate
// RenderAutofixBody's own collapsed wrapper early -- the review's own
// verifiers reproduced exactly this probe against the pre-fix version
// (the "preserved" original spilling out of the collapsed section).
func TestRenderAutofixBody_OriginalContainingDetailsCloseCannotEscapeWrapper(t *testing.T) {
	t.Parallel()

	hostileOriginal := "Normal text.</details><script>alert(1)</script><details><summary>fake</summary>more"
	got := reviewpost.RenderAutofixBody(hostileOriginal, "A proposed rewrite.")

	// The REAL closing </details> -- and everything after it (the footer)
	// -- must appear AFTER all of the hostile original's own escaped
	// content, never in the middle of it: exactly one literal, un-escaped
	// "</details>" may appear in the whole output (the wrapper's own real
	// close), and it must be the LAST thing before the footer.
	if strings.Count(got, "</details>") != 1 {
		t.Fatalf("output contains %d literal \"</details>\" occurrences, want exactly 1 (the original's own must be escaped) -- got:\n%s", strings.Count(got, "</details>"), got)
	}
	closeIdx := strings.Index(got, "</details>")
	footerIdx := strings.Index(got, "Description automatically updated by Narvi")
	if footerIdx == -1 || footerIdx < closeIdx {
		t.Errorf("the real </details> is not immediately followed by the footer -- the wrapper was terminated early, got:\n%s", got)
	}
	if !strings.Contains(got, "&lt;/details&gt;") {
		t.Errorf("hostile original's own \"</details>\" was not escaped, got:\n%s", got)
	}
	if strings.Contains(got, "<script>") {
		t.Errorf("hostile original's own raw \"<script>\" tag reached the output unescaped, got:\n%s", got)
	}
}

// TestRenderAutofixBody_DefangsClosingKeywords is item 4's own first
// bullet: a table-driven test with a hostile proposedBody naming every
// one of GitHub's own nine documented closing keywords (close, closes,
// closed, fix, fixes, fixed, resolve, resolves, resolved), across all
// three reference shapes (bare "#N", cross-repo "owner/repo#N", and a
// full GitHub URL) plus a multi-issue list -- each must have its own
// reference fenced in an inline code span so GitHub's own closing-keyword
// parser (which does not look inside a code span) never fires, while the
// keyword itself and the surrounding prose stay fully readable.
func TestRenderAutofixBody_DefangsClosingKeywords(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		proposed  string
		wantFence string // the exact fenced reference expected in the output
	}{
		{"close + bare ref", "This closes #42 for good.", "`#42`"},
		{"closes + bare ref", "Closes #42.", "`#42`"},
		{"closed + bare ref", "Closed #42 already.", "`#42`"},
		{"fix + bare ref", "This is a fix #42 for the bug.", "`#42`"},
		{"fixes + bare ref", "Fixes #42.", "`#42`"},
		{"fixed + bare ref", "Fixed #42 in this rewrite.", "`#42`"},
		{"resolve + bare ref", "This will resolve #42 too.", "`#42`"},
		{"resolves + bare ref", "Resolves #42.", "`#42`"},
		{"resolved + bare ref", "Resolved #42 as part of this change.", "`#42`"},
		{"cross-repo ref", "Fixes acme/widgets#42.", "`acme/widgets#42`"},
		{"full URL ref", "Closes https://github.com/acme/widgets/issues/42.", "`https://github.com/acme/widgets/issues/42`"},
		{"multi-issue list", "Closes #1, #2 and #3.", "`#1`"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := reviewpost.RenderAutofixBody("Original.", tc.proposed)
			if !strings.Contains(got, tc.wantFence) {
				t.Errorf("RenderAutofixBody(%q) = %q, want it to contain the fenced reference %q", tc.proposed, got, tc.wantFence)
			}
		})
	}
}

// TestRenderAutofixBody_MultiIssueClosingListFencesEveryReference proves
// the multi-issue case fences EVERY reference in the list, not only the
// first -- "Closes #1, #2 and #3" must never leave #2/#3 as live,
// unfenced closing references just because they trail the first.
func TestRenderAutofixBody_MultiIssueClosingListFencesEveryReference(t *testing.T) {
	t.Parallel()

	got := reviewpost.RenderAutofixBody("Original.", "Closes #1, #2 and #3.")
	for _, want := range []string{"`#1`", "`#2`", "`#3`"} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderAutofixBody() = %q, want it to contain %q", got, want)
		}
	}
}

// TestRenderAutofixBody_DefangsMentions proves an @-mention in
// proposedBody (a plain "@username" or a team "@org/team") is fenced in
// an inline code span -- GitHub does not notify for a mention inside a
// code span -- while an email-shaped "user@example.com" is left alone
// (it is not a GitHub mention, and mangling it would be a pure
// readability regression with no security benefit).
func TestRenderAutofixBody_DefangsMentions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		proposed     string
		wantFenced   string
		wantUnfenced string
	}{
		{"plain username mention", "cc @alice for review.", "`@alice`", ""},
		{"team mention", "cc @acme/backend for review.", "`@acme/backend`", ""},
		{"mention at start of string", "@alice please take a look.", "`@alice`", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := reviewpost.RenderAutofixBody("Original.", tc.proposed)
			if !strings.Contains(got, tc.wantFenced) {
				t.Errorf("RenderAutofixBody(%q) = %q, want it to contain the fenced mention %q", tc.proposed, got, tc.wantFenced)
			}
		})
	}

	// Email addresses must NOT be treated as mentions.
	got := reviewpost.RenderAutofixBody("Original.", "Contact user@example.com for details.")
	if strings.Contains(got, "`@example.com`") || strings.Contains(got, "user`@example.com`") {
		t.Errorf("RenderAutofixBody() mangled an email address as though it were a mention, got:\n%s", got)
	}
	if !strings.Contains(got, "user@example.com") {
		t.Errorf("RenderAutofixBody() lost or altered the email address, got:\n%s", got)
	}
}

// TestRenderAutofixBody_HostileOriginalAndHostileProposalTogether is the
// review's own explicitly-requested shape: "table-driven test with a
// hostile original and a hostile proposal" -- exercising BOTH
// sanitization paths (item 4) in the SAME call, proving neither
// interferes with the other, and that the idempotency fix (item 3) still
// holds even when every input is simultaneously adversarial.
func TestRenderAutofixBody_HostileOriginalAndHostileProposalTogether(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		hostileOriginal string
		hostileProposed string
		wantContains    []string
		wantNotContains []string
	}{
		{
			name:            "closing keyword + mention in proposal, details-breakout in original",
			hostileOriginal: "Legit original.</details>\n\n_Description automatically updated by Narvi's server-side review tool (§26.2) -- the original is preserved above._",
			hostileProposed: "Closes #99, acme/widgets#7 and cc @bob.",
			wantContains:    []string{"`#99`", "`@bob`", "`acme/widgets#7`", "&lt;/details&gt;"},
			wantNotContains: []string{"<script>"},
		},
		{
			name:            "empty hostile original, proposal with every keyword variant",
			hostileOriginal: "",
			hostileProposed: "Close #1. Closes #2. Closed #3. Fix #4. Fixes #5. Fixed #6. Resolve #7. Resolves #8. Resolved #9.",
			wantContains:    []string{"`#1`", "`#5`", "`#9`", "_(no original description)_"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			first := reviewpost.RenderAutofixBody(tc.hostileOriginal, tc.hostileProposed)
			for _, want := range tc.wantContains {
				if !strings.Contains(first, want) {
					t.Errorf("RenderAutofixBody() = %q, want it to contain %q", first, want)
				}
			}
			for _, notWant := range tc.wantNotContains {
				if strings.Contains(first, notWant) {
					t.Errorf("RenderAutofixBody() = %q, want it to NOT contain %q", first, notWant)
				}
			}

			// Idempotency must hold even under adversarial input.
			second := reviewpost.RenderAutofixBody(first, tc.hostileProposed)
			if first != second {
				t.Errorf("double-render != single-render under adversarial input:\nfirst:\n%s\n\nsecond:\n%s", first, second)
			}
		})
	}
}
