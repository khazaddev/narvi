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
	// line of the matched source text -- HARD-CODED as a literal, never
	// re-derived from extractFileNewLines itself: that function is exactly
	// what this test exists to catch a line-numbering bug in, and deriving
	// "the expected answer" from the SAME function under test makes the
	// assertion tautological -- a bookkeeping bug there (e.g. a removed
	// line incorrectly advancing the counter) would move BOTH sides of the
	// comparison together, so it could never fail (confirmed by a live
	// mutation: this exact bug left this test, and the whole package,
	// green). sampleDiff is a package-level const in this SAME file, so
	// this literal carries no drift risk of its own: hand-derived from its
	// own hunk header ("@@ -8,5 +10,6 @@", new-file lines start at 10) --
	// logger (10), items (11), the matched "for" line is the third
	// new-file line (the removed "for i := range items {" occupies no
	// position at all) -> 12. TestExtractFileNewLines_LineNumbersAreSequential
	// below is the DEDICATED test for extractFileNewLines' own bookkeeping
	// correctness -- this test deliberately does not try to double as that.
	const wantLine = 12
	if startLine != wantLine {
		t.Errorf("MatchPosition() startLine = %d, want %d (the actual new-file line of the matched text, hand-derived from sampleDiff's own hunk header)", startLine, wantLine)
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

// TestMatchPosition_RejectsWindowStraddlingHunkBoundary is Fix B's own
// regression proof. extractFileNewLines returns a FLAT, non-contiguous
// list of new-file lines -- a hunk boundary just resets the running line
// counter with no marker of its own, so two candidates adjacent in SLICE
// INDEX can be hundreds of real lines apart. twoHunkDiff below has one
// hunk at new-file line 9-10 and a second, unrelated hunk at new-file line
// 400-401 -- candidates end up as [{9,"alpha bravo"}, {10,"charlie
// delta"}, {400,"echo foxtrot"}, {401,"golf hotel"}].
//
// The two-line snippet below is deliberately chosen so its first line
// matches candidates[1] ("charlie delta") perfectly and its second line
// matches candidates[3] ("golf hotel") perfectly, with zero overlap
// otherwise -- BOTH the (index 1, index 2) window and the (index 2, index
// 3) window score identically (0.5 average). Pre-fix, ties break toward
// the EARLIEST index, so the naive sliding window selects (index 1, index
// 2) -- which straddles the hunk boundary and reports (10, 400): a range
// that was never contiguous text in the diff at all (this exact (10, 400)
// shape is what a live repro against this bug confirmed). Post-fix, that
// window is rejected for non-contiguity (candidates[2].LineNo=400 !=
// candidates[1].LineNo+1=11) and the OTHER, fully-in-hunk-2 window
// (index 2, index 3; LineNo 400-401, contiguous) wins instead.
func TestMatchPosition_RejectsWindowStraddlingHunkBoundary(t *testing.T) {
	t.Parallel()

	const twoHunkDiff = `diff --git a/main.go b/main.go
index 1111111..2222222 100644
--- a/main.go
+++ b/main.go
@@ -8,2 +9,2 @@
 alpha bravo
+charlie delta
@@ -399,2 +400,2 @@
 echo foxtrot
+golf hotel
`

	startLine, endLine := MatchPosition("main.go", "charlie delta\ngolf hotel", twoHunkDiff)

	if startLine != 400 || endLine != 401 {
		t.Errorf("MatchPosition() = (%d, %d), want (400, 401) -- either the in-hunk-2 window should win, or (never both) the straddling window (which would report (10, 400), text never actually contiguous in the diff)", startLine, endLine)
	}
}

// TestExtractFileNewLines_AddedLineStartingWithPlusPlusIsNotMistakenForFileHeader
// is Fix E's own regression proof. An added SOURCE line whose own content
// happens to start with "++ " (e.g. a literal "++ counter;") produces a
// diff line "+++ counter;" -- this starts with the same "+++ " prefix as a
// real "+++ b/path" file-header line, but is NOT one. Before this fix, the
// switch matched on the "+++ " PREFIX first and only THEN checked the
// anchored diffFileHeaderRE regex, falling to an else branch that
// incorrectly reset inTargetFile=false/newLineNo=0 -- silently truncating
// extraction for the rest of the file (and every later hunk of it) the
// instant a line like this was seen. This diff's own hunk deliberately
// places a normal added line BEFORE the "+++ counter;" line and another
// normal added line AFTER it, so a regression here shows up as either the
// wrong TOTAL count or the AFTER line going missing entirely.
func TestExtractFileNewLines_AddedLineStartingWithPlusPlusIsNotMistakenForFileHeader(t *testing.T) {
	t.Parallel()

	const diff = `diff --git a/main.go b/main.go
index 1111111..2222222 100644
--- a/main.go
+++ b/main.go
@@ -1,1 +1,4 @@
 package main
+counter := 0
+++ counter;
+fmt.Println(counter)
`

	got := extractFileNewLines(diff, "main.go")
	want := []newFileLine{
		{LineNo: 1, Text: "package main"},
		{LineNo: 2, Text: "counter := 0"},
		{LineNo: 3, Text: "++ counter;"},
		{LineNo: 4, Text: "fmt.Println(counter)"},
	}

	if len(got) != len(want) {
		t.Fatalf("extractFileNewLines() returned %d lines, want %d: got=%v want=%v", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("extractFileNewLines()[%d] = %+v, want %+v", i, got[i], want[i])
		}
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

// TestExtractFileNewLines_LineNumbersAreSequential checks
// extractFileNewLines' own line-numbering bookkeeping against HARD-CODED,
// hand-derived expected values (not merely "each LineNo is one more than
// the previous") -- against BOTH a hunk with no deletions (helper.go) and
// one that DOES contain a deleted line (main.go). The main.go case is
// deliberate and load-bearing: a purely RELATIVE "sequential" check (as
// this test used to be) can never catch a bug where a removed line
// incorrectly ALSO advances the running new-file line counter, because
// that bug shifts every SUBSEQUENT LineNo by a constant offset while
// leaving the deltas BETWEEN consecutive appended lines unchanged at 1 --
// only an exact, absolute comparison against hand-derived values (as
// below) actually observes that kind of uniform shift (confirmed by a
// live mutation against this exact bug class).
func TestExtractFileNewLines_LineNumbersAreSequential(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		filePath string
		want     []newFileLine
	}{
		{
			name:     "hunk with no deletions (helper.go)",
			filePath: "helper.go",
			want: []newFileLine{
				{LineNo: 1, Text: "package helper"},
				{LineNo: 2, Text: "// validateBounds checks the index is within range."},
				{LineNo: 3, Text: "func validateBounds(i int, items []int) bool {"},
				{LineNo: 4, Text: "\treturn i >= 0 && i < len(items)"},
				{LineNo: 5, Text: "}"},
			},
		},
		{
			name:     "hunk WITH a deleted line (main.go)",
			filePath: "main.go",
			want: []newFileLine{
				{LineNo: 10, Text: "\tlogger := setupLogger()"},
				{LineNo: 11, Text: "\titems := fetchItems()"},
				{LineNo: 12, Text: "\tfor i := 0; i < len(items); i++ {"},
				{LineNo: 13, Text: "\t\tvalidateBounds(i, items)"},
				{LineNo: 14, Text: "\t\tprocess(items[i])"},
				{LineNo: 15, Text: "\t}"},
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := extractFileNewLines(sampleDiff, tc.filePath)
			if len(got) != len(tc.want) {
				t.Fatalf("extractFileNewLines(%q) returned %d lines, want %d: got=%v want=%v", tc.filePath, len(got), len(tc.want), got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("extractFileNewLines(%q)[%d] = %+v, want %+v", tc.filePath, i, got[i], tc.want[i])
				}
			}

			// The relative check too, kept as an additional sanity
			// assertion the absolute comparison above already implies when
			// it passes -- not load-bearing on its own (see this test's
			// own doc comment for why), but a clear, minimal repro if it
			// ever regresses independently.
			for i := 1; i < len(got); i++ {
				if got[i].LineNo != got[i-1].LineNo+1 {
					t.Errorf("extractFileNewLines(%q)[%d].LineNo = %d, want %d (sequential from the previous line)", tc.filePath, i, got[i].LineNo, got[i-1].LineNo+1)
				}
			}
		})
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
