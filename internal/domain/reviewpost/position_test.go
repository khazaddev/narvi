package reviewpost

import "testing"

// sampleDiff is a small, realistic two-file unified diff (the exact shape
// internal/app/reviewcontext.Fetch's own GetCompareDiff returns) used
// across this file's table-driven tests. Every hunk body line deliberately
// avoids a truly-blank context line (which real git diff output always
// represents as a single space, never a zero-length line) so hand-derived
// expectations below stay simple and unambiguous.
const sampleDiff = `diff --git a/main.go b/main.go
index 1111111..2222222 100644
--- a/main.go
+++ b/main.go
@@ -8,5 +10,6 @@ func run() error {
 	logger := setupLogger()
 	items := fetchItems()
-	for i := range items {
+	for i := 0; i < len(items); i++ {
+		validateBounds(i, items)
 		process(items[i])
 	}
diff --git a/helper.go b/helper.go
index 3333333..4444444 100644
--- a/helper.go
+++ b/helper.go
@@ -1,3 +1,4 @@
 package helper
+// validateBounds checks the index is within range.
 func validateBounds(i int, items []int) bool {
 	return i >= 0 && i < len(items)
 }
`

func TestMatchPosition_ExactWordOverlapMatchesRightFileAndLine(t *testing.T) {
	t.Parallel()

	startLine, endLine := MatchPosition("main.go", "for i := 0; i < len(items); i++ looks risky", sampleDiff)

	if startLine == 0 {
		t.Fatalf("MatchPosition() startLine = 0, want a real anchored line")
	}
	if startLine != endLine {
		t.Errorf("MatchPosition() startLine=%d endLine=%d, want equal for a single-line snippet", startLine, endLine)
	}

	// Independently verify the returned line number is the ACTUAL new-file
	// line of the matched source text, by re-deriving it from
	// extractFileNewLines directly rather than hard-coding a magic number
	// that would silently drift if sampleDiff's own hunk header changes.
	candidates := extractFileNewLines(sampleDiff, "main.go")
	var wantLine int
	for _, c := range candidates {
		if c.Text == "\tfor i := 0; i < len(items); i++ {" {
			wantLine = c.LineNo
		}
	}
	if wantLine == 0 {
		t.Fatalf("test setup: expected line not found in sampleDiff's own main.go hunk")
	}
	if startLine != wantLine {
		t.Errorf("MatchPosition() startLine = %d, want %d (the actual new-file line of the matched text)", startLine, wantLine)
	}
}

func TestMatchPosition_ScopedToNamedFile(t *testing.T) {
	t.Parallel()

	// "validateBounds checks the index is within range" shares vocabulary
	// with BOTH files (validateBounds is called in main.go and defined/
	// documented in helper.go) -- naming helper.go must anchor inside
	// helper.go's own new-file line range, never main.go's.
	startLine, _ := MatchPosition("helper.go", "validateBounds checks the index is within range for items", sampleDiff)
	if startLine == 0 {
		t.Fatalf("MatchPosition() startLine = 0, want a match inside helper.go")
	}

	helperCandidates := extractFileNewLines(sampleDiff, "helper.go")
	mainCandidates := extractFileNewLines(sampleDiff, "main.go")

	inHelperRange := false
	for _, c := range helperCandidates {
		if c.LineNo == startLine {
			inHelperRange = true
		}
	}
	if !inHelperRange {
		t.Errorf("MatchPosition() startLine = %d is not one of helper.go's own new-file lines %v", startLine, helperCandidates)
	}
	for _, c := range mainCandidates {
		if c.LineNo == startLine {
			t.Errorf("MatchPosition() startLine = %d also happens to be a main.go new-file line -- scoping leaked across files", startLine)
		}
	}
}

func TestMatchPosition_UnrelatedSnippetReturnsZeroZero(t *testing.T) {
	t.Parallel()

	startLine, endLine := MatchPosition("main.go", "completely unrelated prose about quarterly revenue projections", sampleDiff)
	if startLine != 0 || endLine != 0 {
		t.Errorf("MatchPosition() = (%d, %d), want (0, 0) for a snippet sharing no real vocabulary with the diff", startLine, endLine)
	}
}

func TestMatchPosition_UnknownFileReturnsZeroZero(t *testing.T) {
	t.Parallel()

	startLine, endLine := MatchPosition("nonexistent.go", "for i := 0; i < len(items); i++", sampleDiff)
	if startLine != 0 || endLine != 0 {
		t.Errorf("MatchPosition() = (%d, %d), want (0, 0) when filePath never appears in diff", startLine, endLine)
	}
}

func TestMatchPosition_EmptyDiffReturnsZeroZero(t *testing.T) {
	t.Parallel()

	startLine, endLine := MatchPosition("main.go", "for i := 0; i < len(items); i++", "")
	if startLine != 0 || endLine != 0 {
		t.Errorf("MatchPosition() = (%d, %d), want (0, 0) for an empty diff", startLine, endLine)
	}
}

func TestMatchPosition_EmptySnippetReturnsZeroZero(t *testing.T) {
	t.Parallel()

	startLine, endLine := MatchPosition("main.go", "   \n  \n", sampleDiff)
	if startLine != 0 || endLine != 0 {
		t.Errorf("MatchPosition() = (%d, %d), want (0, 0) for a blank snippet", startLine, endLine)
	}
}

// TestMatchPosition_MovedSnippetTracksTheNewLine is this Step's own
// central proof: the SAME snippet, matched against two diffs that differ
// only in how many (unrelated) lines precede the target code, must
// resolve to two DIFFERENT line numbers -- content-anchored positioning
// surviving a pure line shift is the entire point of §22.1.1 (a
// file:line-only identity "breaks the moment a line shifts").
func TestMatchPosition_MovedSnippetTracksTheNewLine(t *testing.T) {
	t.Parallel()

	diffBefore := `diff --git a/main.go b/main.go
index 1111111..2222222 100644
--- a/main.go
+++ b/main.go
@@ -1,2 +1,3 @@
 package main
+	validateBounds(i, items)
 	process(items[i])
`

	diffAfter := `diff --git a/main.go b/main.go
index 1111111..2222222 100644
--- a/main.go
+++ b/main.go
@@ -1,2 +1,8 @@
 package main
+	// padding line one
+	// padding line two
+	// padding line three
+	// padding line four
+	// padding line five
+	validateBounds(i, items)
 	process(items[i])
`

	snippet := "validateBounds(i, items) return value not checked"

	beforeLine, _ := MatchPosition("main.go", snippet, diffBefore)
	afterLine, _ := MatchPosition("main.go", snippet, diffAfter)

	if beforeLine == 0 || afterLine == 0 {
		t.Fatalf("MatchPosition() failed to anchor in one of the two diffs: before=%d after=%d", beforeLine, afterLine)
	}
	if beforeLine == afterLine {
		t.Fatalf("MatchPosition() returned the SAME line (%d) for both diffs -- the whole point of this test is that the target line MOVED", beforeLine)
	}
	if afterLine <= beforeLine {
		t.Errorf("MatchPosition() afterLine=%d, want strictly greater than beforeLine=%d (the padding pushed the target line DOWN)", afterLine, beforeLine)
	}
}

func TestExtractFileNewLines_SkipsRemovedLines(t *testing.T) {
	t.Parallel()

	candidates := extractFileNewLines(sampleDiff, "main.go")
	for _, c := range candidates {
		if c.Text == "\tfor i := range items {" {
			t.Errorf("extractFileNewLines() included a REMOVED line (%q) -- removed lines occupy no position in the new file", c.Text)
		}
	}
}

func TestExtractFileNewLines_LineNumbersAreSequential(t *testing.T) {
	t.Parallel()

	candidates := extractFileNewLines(sampleDiff, "helper.go")
	if len(candidates) == 0 {
		t.Fatalf("extractFileNewLines() returned nothing for helper.go")
	}
	for i := 1; i < len(candidates); i++ {
		if candidates[i].LineNo != candidates[i-1].LineNo+1 {
			t.Errorf("extractFileNewLines()[%d].LineNo = %d, want %d (sequential from the previous line)", i, candidates[i].LineNo, candidates[i-1].LineNo+1)
		}
	}
}

func TestLineScore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		snippetLine   string
		candidateLine string
		wantZero      bool
		wantPositive  bool
	}{
		{"identical text scores positive", "for i := 0; i < len(items); i++", "for i := 0; i < len(items); i++", false, true},
		{"no shared significant words scores zero", "quarterly revenue projections", "func main() error", true, false},
		{"empty snippet line scores zero", "", "func main() error", true, false},
		{"empty candidate line scores zero", "func main() error", "", true, false},
		{"partial overlap scores positive but not necessarily full", "validateBounds checks the index range", "func validateBounds(i int) bool", false, true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			score := lineScore(tc.snippetLine, tc.candidateLine)
			if tc.wantZero && score != 0 {
				t.Errorf("lineScore(%q, %q) = %v, want 0", tc.snippetLine, tc.candidateLine, score)
			}
			if tc.wantPositive && score <= 0 {
				t.Errorf("lineScore(%q, %q) = %v, want > 0", tc.snippetLine, tc.candidateLine, score)
			}
			if score < 0 || score > 1 {
				t.Errorf("lineScore(%q, %q) = %v, want in [0, 1]", tc.snippetLine, tc.candidateLine, score)
			}
		})
	}
}

func TestSignificantWords(t *testing.T) {
	t.Parallel()

	got := significantWords("The validateBounds fix (a=1) is GOOD")
	wantPresent := []string{"the", "validatebounds", "fix", "good"}
	for _, w := range wantPresent {
		if _, ok := got[w]; !ok {
			t.Errorf("significantWords() missing %q; got %v", w, got)
		}
	}
	// Short tokens ("a", "is") must be excluded.
	for _, w := range []string{"a", "is"} {
		if _, ok := got[w]; ok {
			t.Errorf("significantWords() unexpectedly included short token %q; got %v", w, got)
		}
	}
}
