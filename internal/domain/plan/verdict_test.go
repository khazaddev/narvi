package plan

import "testing"

// TestMatchVerdict is table-driven (§11) over the exact chosen keyword set
// (Step 38, "plan mode, cross-channel") -- approve variants, reject
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
