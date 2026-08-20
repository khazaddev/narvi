package plan

import "testing"

// TestMatchVerdict is table-driven (§11) over the exact chosen keyword set
// (§8.1, "plan mode, cross-channel") -- approve variants, reject
// variants, case/whitespace insensitivity, and confirms any other text
// (including a superstring containing a keyword) does NOT match, matching
// this file's own doc comment on why substring matching is deliberately
// refused.
func TestMatchVerdict(t *testing.T) {
	tests := []struct {
		name        string
		text        string
		wantVerdict string
		wantOK      bool
	}{
		{name: "approve exact", text: "approve", wantVerdict: "approve", wantOK: true},
		{name: "approved exact", text: "approved", wantVerdict: "approve", wantOK: true},
		{name: "lgtm exact", text: "lgtm", wantVerdict: "approve", wantOK: true},
		{name: "approve uppercase", text: "APPROVE", wantVerdict: "approve", wantOK: true},
		{name: "approve mixed case with whitespace", text: "  Approved  ", wantVerdict: "approve", wantOK: true},
		{name: "reject exact", text: "reject", wantVerdict: "reject", wantOK: true},
		{name: "rejected exact", text: "rejected", wantVerdict: "reject", wantOK: true},
		{name: "no exact", text: "no", wantVerdict: "reject", wantOK: true},
		{name: "reject uppercase with whitespace", text: "\tNO\n", wantVerdict: "reject", wantOK: true},
		{name: "empty text", text: "", wantOK: false},
		{name: "whitespace only", text: "   ", wantOK: false},
		{name: "ordinary feedback text falls through", text: "keep the env fallback please", wantOK: false},
		{name: "superstring containing approve keyword is not a match", text: "I don't think we should reject this, let's approve it once X is fixed", wantOK: false},
		{name: "superstring containing lgtm is not a match", text: "lgtm but wait", wantOK: false},
		{name: "unrelated single word", text: "maybe", wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotVerdict, gotOK := MatchVerdict(tc.text)
			if gotOK != tc.wantOK {
				t.Fatalf("MatchVerdict(%q) ok = %v, want %v", tc.text, gotOK, tc.wantOK)
			}
			if gotOK && gotVerdict != tc.wantVerdict {
				t.Errorf("MatchVerdict(%q) verdict = %q, want %q", tc.text, gotVerdict, tc.wantVerdict)
			}
		})
	}
}

// TestMatchRevise is table-driven (§11) over RevisePrefix: prefix match at
// the start of the (trimmed) reply, case/whitespace insensitivity in the
// prefix itself, whitespace trimmed off the extracted feedback, empty
// feedback after a bare prefix, and confirms a reply that merely MENTIONS
// "revise" without the reply STARTING with it does NOT match -- matching
// this file's own doc comment on why this is a prefix check, never a
// substring/contains one.
func TestMatchRevise(t *testing.T) {
	tests := []struct {
		name         string
		text         string
		wantFeedback string
		wantOK       bool
	}{
		{name: "prefix with feedback", text: "revise: drop the retry", wantFeedback: "drop the retry", wantOK: true},
		{name: "uppercase prefix", text: "REVISE: drop the retry", wantFeedback: "drop the retry", wantOK: true},
		{name: "mixed case prefix with surrounding whitespace", text: "  Revise:   drop the retry  ", wantFeedback: "drop the retry", wantOK: true},
		{name: "no space after colon", text: "revise:drop the retry", wantFeedback: "drop the retry", wantOK: true},
		{name: "bare prefix, empty feedback", text: "revise:", wantFeedback: "", wantOK: true},
		{name: "bare prefix, whitespace-only feedback", text: "revise:   ", wantFeedback: "", wantOK: true},
		// COSMETIC/LOW audit fix (confirmed findings: the regression suite
		// only ever covered ASCII-space whitespace-only feedback, despite
		// this batch's own code comments explicitly claiming the fix
		// "treats whitespace-only feedback as empty too" -- implying
		// tabs/newlines/NBSP/unicode whitespace generally). strings.
		// TrimSpace/unicode.IsSpace already handle every one of these
		// correctly (see MatchRevise's own implementation), so these cases
		// pass today -- their purpose is pinning that behavior against a
		// FUTURE narrowing regression (e.g. swapping in a hand-rolled
		// ASCII-only trim), which none of the pre-existing cases above
		// would have caught.
		{name: "bare prefix, tab-only feedback", text: "revise:\t\t", wantFeedback: "", wantOK: true},
		{name: "bare prefix, newline-only feedback", text: "revise:\n\n", wantFeedback: "", wantOK: true},
		{name: "bare prefix, NBSP-only feedback (U+00A0)", text: "revise:\u00A0\u00A0", wantFeedback: "", wantOK: true},
		{name: "bare prefix, ideographic-space-only feedback (U+3000)", text: "revise:\u3000\u3000", wantFeedback: "", wantOK: true},
		{name: "bare prefix, mixed tab/newline/NBSP-only feedback", text: "revise: \t\n\u00A0 ", wantFeedback: "", wantOK: true},
		// LOW audit fix (confirmed finding, "MatchRevise's feedback-
		// emptiness check ... does not treat zero-width characters as
		// whitespace"): unicode.IsSpace does NOT classify these as
		// whitespace (they're Unicode category Cf, format characters, not
		// Zs space separators), so MatchRevise's own strings.TrimSpace
		// leaves them in feedback untouched -- this is MatchRevise's own
		// correct, documented behavior (deciding what counts as "empty" is
		// the CALLER's job, this file's own top doc comment); the actual
		// emptiness gate is IsBlankFeedback, exercised in TestIsBlankFeedback
		// below. Pinned here too so a future change to MatchRevise itself
		// can't silently start stripping these without a test noticing.
		{name: "bare prefix, zero-width-space feedback is NOT stripped by MatchRevise itself", text: "revise:\u200B", wantFeedback: "\u200B", wantOK: true},
		{name: "empty text", text: "", wantOK: false},
		{name: "whitespace only", text: "   ", wantOK: false},
		{name: "ordinary text with no prefix at all", text: "keep the env fallback please", wantOK: false},
		{name: "mentions revise mid-sentence, not a prefix", text: "let's revise: the approach later", wantOK: false},
		{name: "approve keyword is not a revise match", text: "approve", wantOK: false},
		// Regression case for the MEDIUM audit finding (Unicode byte-offset
		// bug): İ (LATIN CAPITAL LETTER I WITH DOT ABOVE, U+0130) is 2 bytes
		// in UTF-8 but strings.ToLower's simple case mapping folds it to
		// plain ASCII "i" (1 byte) -- the OLD implementation lower-cased a
		// COPY of the trimmed string to check the prefix, then sliced the
		// ORIGINAL (un-folded, still-2-byte-İ) string at len(RevisePrefix)
		// BYTES, landing one byte short of the real prefix boundary and
		// leaking the trailing ":" into feedback (": drop the retry", not
		// "drop the retry"). Proves the fix returns the ORIGINAL bytes
		// after the prefix, byte-for-byte, with no leaked prefix bytes.
		{name: "dotted capital I case-fold byte-length change (İ)", text: "revİse: drop the retry", wantFeedback: "drop the retry", wantOK: true},
		// A second, harder variant of the same bug: EVERY ASCII letter in
		// the prefix replaced by its Turkish-locale-style all-caps form
		// (including the dotted capital I), proving the rune-by-rune match
		// still consumes exactly len(RevisePrefix) runes -- never bytes --
		// even when the byte-length-changing rune is not the only one being
		// case-folded.
		{name: "dotted capital I, whole prefix uppercased", text: "REVİSE: drop the retry", wantFeedback: "drop the retry", wantOK: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotFeedback, gotOK := MatchRevise(tc.text)
			if gotOK != tc.wantOK {
				t.Fatalf("MatchRevise(%q) ok = %v, want %v", tc.text, gotOK, tc.wantOK)
			}
			if gotOK && gotFeedback != tc.wantFeedback {
				t.Errorf("MatchRevise(%q) feedback = %q, want %q", tc.text, gotFeedback, tc.wantFeedback)
			}
		})
	}
}

// TestIsBlankFeedback is table-driven (§11) over IsBlankFeedback -- the
// audit-remediation batch's own LOW-finding regression test (confirmed
// finding, "MatchRevise's feedback-emptiness check ... does not treat
// zero-width characters as whitespace"): pins that IsBlankFeedback treats
// BOTH ordinary Unicode whitespace (tab, newline, NBSP U+00A0, ideographic
// space U+3000 -- exactly like strings.TrimSpace already did) AND the
// zero-width runes isZeroWidthRune recognizes (U+200B/200C/200D/FEFF, in
// any combination, including mixed with ordinary whitespace) as blank, and
// that genuinely visible feedback -- even feedback that merely CONTAINS a
// zero-width rune alongside real content -- is never misreported as blank.
func TestIsBlankFeedback(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "empty string", text: "", want: true},
		{name: "ascii spaces only", text: "   ", want: true},
		{name: "tab only", text: "\t\t", want: true},
		{name: "newline only", text: "\n\n", want: true},
		{name: "NBSP only (U+00A0)", text: "\u00A0\u00A0", want: true},
		{name: "ideographic space only (U+3000)", text: "\u3000", want: true},
		{name: "mixed ordinary whitespace only", text: " \t\n \u3000 ", want: true},
		{name: "zero width space only (U+200B)", text: "\u200B", want: true},
		{name: "zero width non-joiner only (U+200C)", text: "\u200C", want: true},
		{name: "zero width joiner only (U+200D)", text: "\u200D", want: true},
		{name: "zero width no-break space / BOM only (U+FEFF)", text: "\uFEFF", want: true},
		{name: "multiple zero-width runes only", text: "\u200B\u200C\u200D\uFEFF", want: true},
		{name: "zero-width runes mixed with ordinary whitespace only", text: " \u200B\t\uFEFF\n ", want: true},
		{name: "genuine visible feedback", text: "drop the retry", want: false},
		{name: "visible feedback padded with ordinary whitespace", text: "  drop the retry  ", want: false},
		{name: "visible feedback with a zero-width rune inside it is still non-blank", text: "drop\u200Bthe retry", want: false},
		{name: "single visible character amid zero-width runes is still non-blank", text: "\u200Bx\uFEFF", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsBlankFeedback(tc.text)
			if got != tc.want {
				t.Errorf("IsBlankFeedback(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}
