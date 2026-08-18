package reviewpost_test

import (
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/reviewpost"
)

const currentFileContent = `package foo

func Bar() int {
	return 1
}

func Baz() int {
	return 2
}
`

func validPatch() string {
	return strings.Join([]string{
		"--- a/internal/foo/bar.go",
		"+++ b/internal/foo/bar.go",
		"@@ -1,5 +1,7 @@",
		" package foo",
		" ",
		" func Bar() int {",
		"-\treturn 1",
		"+\treturn 1 // fixed",
		" }",
	}, "\n")
}

func TestValidateSuggestionApplies_TableDriven(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		content string
		patch   string
		wantErr error
	}{
		{
			name:    "valid patch applies cleanly",
			path:    "internal/foo/bar.go",
			content: currentFileContent,
			patch:   validPatch(),
			wantErr: nil,
		},
		{
			name:    "empty patch rejected",
			path:    "internal/foo/bar.go",
			content: currentFileContent,
			patch:   "   ",
			wantErr: reviewpost.ErrSuggestionEmpty,
		},
		{
			name:    "patch with no hunks rejected",
			path:    "internal/foo/bar.go",
			content: currentFileContent,
			patch:   "--- a/internal/foo/bar.go\n+++ b/internal/foo/bar.go\n",
			wantErr: reviewpost.ErrSuggestionNoHunks,
		},
		{
			name:    "patch targeting a DIFFERENT file is rejected (out-of-scope)",
			path:    "internal/foo/bar.go",
			content: currentFileContent,
			patch: strings.Join([]string{
				"--- a/internal/other/unrelated.go",
				"+++ b/internal/other/unrelated.go",
				"@@ -1,1 +1,1 @@",
				"-old",
				"+new",
			}, "\n"),
			wantErr: reviewpost.ErrSuggestionWrongFile,
		},
		{
			name:    "patch whose old lines no longer match current content is stale",
			path:    "internal/foo/bar.go",
			content: currentFileContent,
			patch: strings.Join([]string{
				"--- a/internal/foo/bar.go",
				"+++ b/internal/foo/bar.go",
				"@@ -1,3 +1,3 @@",
				" package foo",
				"-func ThisFunctionDoesNotExist() int {",
				"+func ThisFunctionDoesNotExistFixed() int {",
			}, "\n"),
			wantErr: reviewpost.ErrSuggestionStale,
		},
		{
			name:    "pure-insertion hunk (no old lines) is trivially satisfied",
			path:    "internal/foo/bar.go",
			content: currentFileContent,
			patch: strings.Join([]string{
				"--- a/internal/foo/bar.go",
				"+++ b/internal/foo/bar.go",
				"@@ -9,0 +10,3 @@",
				"+",
				"+func Qux() int {",
				"+}",
			}, "\n"),
			wantErr: nil,
		},
		{
			name:    "no diff header at all is not rejected for wrong-file (nothing to contradict)",
			path:    "internal/foo/bar.go",
			content: currentFileContent,
			patch: strings.Join([]string{
				"@@ -1,1 +1,1 @@",
				"-package foo",
				"+package foo // comment",
			}, "\n"),
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := reviewpost.ValidateSuggestionApplies(tt.path, tt.content, tt.patch)
			if err != tt.wantErr {
				t.Errorf("ValidateSuggestionApplies() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestApplySuggestionPatch_ProducesExpectedContent(t *testing.T) {
	got, err := reviewpost.ApplySuggestionPatch(currentFileContent, validPatch())
	if err != nil {
		t.Fatalf("ApplySuggestionPatch() error = %v", err)
	}
	if !strings.Contains(got, "return 1 // fixed") {
		t.Errorf("ApplySuggestionPatch() = %q, want it to contain the patched line", got)
	}
	if strings.Contains(got, "\treturn 1\n") {
		t.Errorf("ApplySuggestionPatch() = %q, still contains the OLD line, want it replaced", got)
	}
	// Everything outside the hunk is untouched.
	if !strings.Contains(got, "func Baz() int {\n\treturn 2\n}") {
		t.Errorf("ApplySuggestionPatch() = %q, want Baz() left untouched", got)
	}
}

func TestApplySuggestionPatch_PureInsertionAppends(t *testing.T) {
	patch := strings.Join([]string{
		"--- a/internal/foo/bar.go",
		"+++ b/internal/foo/bar.go",
		"@@ -9,0 +10,3 @@",
		"+",
		"+func Qux() int {",
		"+}",
	}, "\n")

	got, err := reviewpost.ApplySuggestionPatch(currentFileContent, patch)
	if err != nil {
		t.Fatalf("ApplySuggestionPatch() error = %v", err)
	}
	if !strings.Contains(got, "func Qux() int {\n}") {
		t.Errorf("ApplySuggestionPatch() = %q, want the inserted function appended", got)
	}
	// The original content must still be a PREFIX of the result -- a pure
	// insertion must never mutate anything that came before it.
	if !strings.HasPrefix(got, currentFileContent) {
		t.Errorf("ApplySuggestionPatch() = %q, want %q as a prefix", got, currentFileContent)
	}
}

// repeatedBlockContent has the SAME two-line block ("\treturn nil\n}")
// appear three times -- a Go file's own common shape, and exactly the
// concrete failure case named in the audit finding this test guards
// against: a coverage-style SuggestedFix whose hunk context is a common
// block and targets a specific (here, the 3rd) occurrence.
const repeatedBlockContent = `package foo

func A() error {
	return nil
}

func B() error {
	return nil
}

func C() error {
	return nil
}
`

// doubleBlankContent has two CONSECUTIVE blank lines between A and B (and
// other, unrelated blank lines elsewhere in the file) -- a file shape
// where a hunk that removes exactly one blank line, with no context lines
// of its own, produces an "old" block that is textually ambiguous (a
// single blank line matches several places in the file) and, before this
// fix, degenerated to the empty string and was misclassified as a pure
// insertion.
const doubleBlankContent = "package foo\n\nfunc A() int {\n\treturn 1\n}\n\n\nfunc B() int {\n\treturn 2\n}\n"

func TestApplySuggestionPatch_PositionResolution_TableDriven(t *testing.T) {
	tests := []struct {
		name            string
		content         string
		patch           string
		wantErr         error
		wantExact       string   // if non-empty, got must equal this exactly
		wantContains    []string // got must contain every one of these
		wantNotContains []string // got must contain NONE of these
		wantNotSuffix   string   // got must NOT end with this (distinguishes "at the right line" from "appended at EOF")
	}{
		{
			// The hunk's own header names the 3rd occurrence's real line
			// number (12). Before the fix, ApplySuggestionPatch used
			// strings.Replace(result, h.Old, h.New, 1), which always
			// rewrites the FIRST occurrence (A's block) regardless of
			// what the hunk actually targeted -- silently patching the
			// wrong function.
			name:    "repeated block: patches the occurrence the header names, not the first",
			content: repeatedBlockContent,
			patch: strings.Join([]string{
				"--- a/internal/foo/bar.go",
				"+++ b/internal/foo/bar.go",
				"@@ -12,2 +12,2 @@",
				"-\treturn nil",
				"-}",
				"+\treturn errC",
				"+}",
			}, "\n"),
			wantContains: []string{
				"func A() error {\n\treturn nil\n}",  // untouched
				"func B() error {\n\treturn nil\n}",  // untouched
				"func C() error {\n\treturn errC\n}", // the ONE that should change
			},
			wantNotContains: []string{
				"func C() error {\n\treturn nil\n}",
			},
		},
		{
			// A pure-insertion hunk (no old lines) whose header names a
			// position in the MIDDLE of the file, not end-of-file.
			// Before the fix, every pure-insertion hunk hit the
			// `h.Old == ""` branch and was unconditionally appended at
			// EOF, ignoring what the "@@" header actually said.
			name:    "pure insertion lands at the line the header names, not at EOF",
			content: repeatedBlockContent,
			patch: strings.Join([]string{
				"--- a/internal/foo/bar.go",
				"+++ b/internal/foo/bar.go",
				"@@ -5,0 +6,1 @@",
				"+// inserted",
			}, "\n"),
			wantContains: []string{
				"}\n// inserted\n\nfunc B() error {",
			},
			// If it had been appended at EOF instead (the pre-fix
			// behavior for every pure-insertion hunk), the file would
			// END with the inserted line.
			wantNotSuffix: "// inserted\n",
		},
		{
			// A hunk whose "old" side is a single removed blank line
			// (oldLines == [""], which strings.Join collapses to "").
			// Before the fix this hit the same `h.Old == ""` branch as a
			// pure insertion and appended (nothing, since h.New was also
			// "") at EOF -- the blank line was never actually removed,
			// so the double-blank survives unchanged.
			name:    "blank-line-only deletion removes the line the header names",
			content: doubleBlankContent,
			patch: strings.Join([]string{
				"--- a/internal/foo/bar.go",
				"+++ b/internal/foo/bar.go",
				"@@ -7,1 +7,0 @@",
				"-",
			}, "\n"),
			wantExact: "package foo\n\nfunc A() int {\n\treturn 1\n}\n\nfunc B() int {\n\treturn 2\n}\n",
		},
		{
			// The old block ("\treturn nil\n}") genuinely matches three
			// places, and the header names a line (100) that doesn't
			// correspond to ANY of them -- position can't be determined,
			// so this must fail closed rather than guess (e.g. by
			// silently falling back to "first occurrence").
			name:    "ambiguous position with no header match fails closed",
			content: repeatedBlockContent,
			patch: strings.Join([]string{
				"--- a/internal/foo/bar.go",
				"+++ b/internal/foo/bar.go",
				"@@ -100,2 +100,2 @@",
				"-\treturn nil",
				"-}",
				"+\treturn errX",
				"+}",
			}, "\n"),
			wantErr: reviewpost.ErrSuggestionAmbiguous,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := reviewpost.ApplySuggestionPatch(tt.content, tt.patch)
			if tt.wantErr != nil {
				if err != tt.wantErr {
					t.Fatalf("ApplySuggestionPatch() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ApplySuggestionPatch() unexpected error = %v", err)
			}
			if tt.wantExact != "" && got != tt.wantExact {
				t.Errorf("ApplySuggestionPatch() = %q, want exactly %q", got, tt.wantExact)
			}
			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("ApplySuggestionPatch() = %q, want it to contain %q", got, want)
				}
			}
			for _, notWant := range tt.wantNotContains {
				if strings.Contains(got, notWant) {
					t.Errorf("ApplySuggestionPatch() = %q, want it NOT to contain %q", got, notWant)
				}
			}
			if tt.wantNotSuffix != "" && strings.HasSuffix(got, tt.wantNotSuffix) {
				t.Errorf("ApplySuggestionPatch() = %q, want it NOT to end with %q", got, tt.wantNotSuffix)
			}
		})
	}
}

// TestValidateSuggestionApplies_AmbiguousPositionRejected confirms
// ValidateSuggestionApplies itself -- not just ApplySuggestionPatch --
// rejects a patch whose position can't be pinned down, so a caller can
// never observe validation succeed and the apply then fail (or, before
// this fix, silently apply to the wrong place).
func TestValidateSuggestionApplies_AmbiguousPositionRejected(t *testing.T) {
	patch := strings.Join([]string{
		"--- a/internal/foo/bar.go",
		"+++ b/internal/foo/bar.go",
		"@@ -100,2 +100,2 @@",
		"-\treturn nil",
		"-}",
		"+\treturn errX",
		"+}",
	}, "\n")

	err := reviewpost.ValidateSuggestionApplies("internal/foo/bar.go", repeatedBlockContent, patch)
	if err != reviewpost.ErrSuggestionAmbiguous {
		t.Errorf("ValidateSuggestionApplies() error = %v, want %v", err, reviewpost.ErrSuggestionAmbiguous)
	}
}

// TestValidateSuggestionApplies_RepeatedBlockWithCorrectHeaderAccepted
// confirms that a hunk targeting a repeated block is NOT rejected just
// because the block is repeated -- only when the header can't disambiguate
// which occurrence is meant.
func TestValidateSuggestionApplies_RepeatedBlockWithCorrectHeaderAccepted(t *testing.T) {
	patch := strings.Join([]string{
		"--- a/internal/foo/bar.go",
		"+++ b/internal/foo/bar.go",
		"@@ -12,2 +12,2 @@",
		"-\treturn nil",
		"-}",
		"+\treturn errC",
		"+}",
	}, "\n")

	if err := reviewpost.ValidateSuggestionApplies("internal/foo/bar.go", repeatedBlockContent, patch); err != nil {
		t.Errorf("ValidateSuggestionApplies() error = %v, want nil", err)
	}
}
