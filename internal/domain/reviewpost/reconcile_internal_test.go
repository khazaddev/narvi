package reviewpost

import "testing"

// TestFindingInDiff_ReconciliationIsConfidentNotMerelyAmbiguous is a
// white-box (package reviewpost, not reviewpost_test) pin on
// findingInDiff's own (member, unknown) return semantics -- a black-box
// test of RenderAlreadyAnsweredFacts alone cannot distinguish "confidently
// a member" from "ambiguous, so not retired anyway" (both produce the
// identical observable "no RETIRED marker" outcome), but the two are NOT
// the same claim: D3's own reconcileDiffVocabulary step exists precisely
// so a diff-header-prefixed self-reported path is recognized as a
// CONFIDENT match, not merely an unresolved ambiguity that happens to
// share the same fail-safe outcome as a genuine case-only mismatch. This
// test pins that distinction directly, so a mutation that silently
// degrades a confident reconciliation into an ambiguity (removing the
// reconciled-exact-match check while leaving the case-insensitive
// fallback in place, which happens to produce the SAME retire/no-retire
// answer for every case below) is still caught.
func TestFindingInDiff_ReconciliationIsConfidentNotMerelyAmbiguous(t *testing.T) {
	idx := buildChangedPathIndex([]string{"internal/foo.go"}, false)
	if idx == nil {
		t.Fatal("buildChangedPathIndex() = nil, want a real index")
	}

	tests := []struct {
		name        string
		filePath    string
		wantMember  bool
		wantUnknown bool
	}{
		{name: "exact spelling", filePath: "internal/foo.go", wantMember: true, wantUnknown: false},
		{name: "b/ prefix reconciles to a CONFIDENT match", filePath: "b/internal/foo.go", wantMember: true, wantUnknown: false},
		{name: "a/ prefix reconciles to a CONFIDENT match", filePath: "a/internal/foo.go", wantMember: true, wantUnknown: false},
		{name: "leading slash reconciles to a CONFIDENT match", filePath: "/internal/foo.go", wantMember: true, wantUnknown: false},
		{name: `backslash separators reconcile to a CONFIDENT match`, filePath: `internal\foo.go`, wantMember: true, wantUnknown: false},
		{name: "case-only difference is a genuine AMBIGUITY, never a confident match", filePath: "Internal/Foo.go", wantMember: false, wantUnknown: true},
		{name: "unrelated path is a CONFIDENT non-match", filePath: "internal/unrelated.go", wantMember: false, wantUnknown: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMember, gotUnknown := findingInDiff(tt.filePath, idx)
			if gotMember != tt.wantMember || gotUnknown != tt.wantUnknown {
				t.Errorf("findingInDiff(%q) = (member=%v, unknown=%v), want (member=%v, unknown=%v)", tt.filePath, gotMember, gotUnknown, tt.wantMember, tt.wantUnknown)
			}
		})
	}
}
