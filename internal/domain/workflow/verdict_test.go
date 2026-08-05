package workflow

import "testing"

// TestMatchEdit is table-driven (§11) over EditPrefix, mirroring
// internal/domain/plan's own TestMatchRevise exactly: prefix match at the
// start of the (trimmed) text, case/whitespace insensitivity in the prefix
// itself, whitespace trimmed off the extracted feedback, empty feedback
// after a bare prefix, confirms text that merely MENTIONS "edit" without
// STARTING with it does not match, and a rune-safety regression case
// mirroring plan.MatchRevise's own documented Unicode byte-offset bug fix.
func TestMatchEdit(t *testing.T) {
	tests := []struct {
		name         string
		text         string
		wantFeedback string
		wantOK       bool
	}{
		{name: "prefix with feedback", text: "edit: drop the retry", wantFeedback: "drop the retry", wantOK: true},
		{name: "uppercase prefix", text: "EDIT: drop the retry", wantFeedback: "drop the retry", wantOK: true},
		{name: "mixed case prefix with surrounding whitespace", text: "  Edit:   drop the retry  ", wantFeedback: "drop the retry", wantOK: true},
		{name: "no space after colon", text: "edit:drop the retry", wantFeedback: "drop the retry", wantOK: true},
		{name: "bare prefix, empty feedback", text: "edit:", wantFeedback: "", wantOK: true},
		{name: "bare prefix, whitespace-only feedback", text: "edit:   ", wantFeedback: "", wantOK: true},
		{name: "bare prefix, tab-only feedback", text: "edit:\t\t", wantFeedback: "", wantOK: true},
		{name: "bare prefix, newline-only feedback", text: "edit:\n\n", wantFeedback: "", wantOK: true},
		{name: "empty text", text: "", wantOK: false},
		{name: "whitespace only", text: "   ", wantOK: false},
		{name: "ordinary text with no prefix at all", text: "keep the env fallback please", wantOK: false},
		{name: "mentions edit mid-sentence, not a prefix", text: "let's edit: the approach later", wantOK: false},
		{name: "approve keyword is not an edit match", text: "approve", wantOK: false},
		{name: "plan's own revise prefix is not an edit match", text: "revise: drop the retry", wantOK: false},
		// Regression case mirroring plan.MatchRevise's own documented
		// Unicode byte-offset bug fix (verdict_test.go, internal/domain/
		// plan): İ (LATIN CAPITAL LETTER I WITH DOT ABOVE, U+0130) is 2
		// bytes in UTF-8 but strings.ToLower's simple case mapping folds it
		// to plain ASCII "i" (1 byte) -- exactly the rune EditPrefix's own
		// "edit:" contains. A naive "lower-case a copy to check the prefix,
		// then slice the ORIGINAL at len(EditPrefix) BYTES" implementation
		// would land one byte short of the real prefix boundary here,
		// leaking the trailing ":" into feedback (": drop the retry", not
		// "drop the retry"). Proves MatchEdit's rune-by-rune match returns
		// the ORIGINAL bytes after the prefix, byte-for-byte, with no
		// leaked prefix bytes.
		{name: "dotted capital I case-fold byte-length change (İ)", text: "edİt: drop the retry", wantFeedback: "drop the retry", wantOK: true},
		// A second, harder variant: EVERY ASCII letter in the prefix
		// replaced by its Turkish-locale-style all-caps form (including the
		// dotted capital I), proving the rune-by-rune match still consumes
		// exactly len(EditPrefix) runes -- never bytes -- even when the
		// byte-length-changing rune is not the only one being case-folded.
		{name: "dotted capital I, whole prefix uppercased", text: "EDİT: drop the retry", wantFeedback: "drop the retry", wantOK: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotFeedback, gotOK := MatchEdit(tc.text)
			if gotOK != tc.wantOK {
				t.Fatalf("MatchEdit(%q) ok = %v, want %v", tc.text, gotOK, tc.wantOK)
			}
			if gotOK && gotFeedback != tc.wantFeedback {
				t.Errorf("MatchEdit(%q) feedback = %q, want %q", tc.text, gotFeedback, tc.wantFeedback)
			}
		})
	}
}
