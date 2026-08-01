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
