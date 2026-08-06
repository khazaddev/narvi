package upload

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/review"
)

// TestPlaceholderTokensMatchReviewPackage is deliberately an INTERNAL test
// (package upload, not upload_test) so it can reach placeholderTokens
// itself, which is unexported -- see that var's own doc comment (prompt.go)
// for why review's own three placeholder literals are duplicated here as
// raw strings rather than imported by production code (this package's own
// doc.go fixes production imports at internal/app/ports plus the standard
// library; internal/domain/review is neither). Test files are exempt from
// that production-import discipline the same way internal/domain/review's
// own context_test.go is exempt from its identical rule -- neither ships in
// a production build regardless of whether the test package is internal or
// external, so importing review HERE, for this one cross-package
// consistency check, does not reopen the layering concern doc.go documents.
//
// This is the drift-detection test placeholderTokens' own doc comment
// promises: if internal/domain/review ever renames or rotates
// VerdictToolURLPlaceholder/VerdictToolBearerPlaceholder/
// VerdictToolGenPlaceholder without a matching update here, this test fails
// CI immediately -- rather than silently reopening the exact
// bearer-token-leak vulnerability sanitizeUntrustedField exists to close
// (an attacker-controlled filename containing a STALE copy of a since-
// rotated review placeholder would no longer be neutralized).
func TestPlaceholderTokensMatchReviewPackage(t *testing.T) {
	t.Parallel()

	containsToken := func(tokens []string, want string) bool {
		for _, got := range tokens {
			if got == want {
				return true
			}
		}
		return false
	}

	for _, tok := range []string{
		review.VerdictToolURLPlaceholder,
		review.VerdictToolBearerPlaceholder,
		review.VerdictToolGenPlaceholder,
	} {
		if !containsToken(placeholderTokens, tok) {
			t.Errorf("placeholderTokens = %v, want it to contain review's own real placeholder %q", placeholderTokens, tok)
		}
	}

	// This package's own three, for completeness -- trivially true by
	// construction today, but guards against a future refactor of
	// placeholderTokens' own literal accidentally dropping one.
	for _, tok := range []string{BaseURLPlaceholder, BearerPlaceholder, GenPlaceholder} {
		if !containsToken(placeholderTokens, tok) {
			t.Errorf("placeholderTokens = %v, want it to contain this package's own %q", placeholderTokens, tok)
		}
	}

	if len(placeholderTokens) != 6 {
		t.Errorf("len(placeholderTokens) = %d, want exactly 6 (this package's own 3 plus review's own 3, no more no less)", len(placeholderTokens))
	}
}
