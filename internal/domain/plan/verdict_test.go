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
