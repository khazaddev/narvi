package upload

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/domain/turn"
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

	for _, tok := range []string{
		review.VerdictToolURLPlaceholder,
		review.VerdictToolBearerPlaceholder,
		review.VerdictToolGenPlaceholder,
	} {
		if !containsToken(placeholderTokens, tok) {
			t.Errorf("placeholderTokens = %v, want it to contain review's own real placeholder %q", placeholderTokens, tok)
		}
	}
}

// TestPlaceholderTokensMatchTurnPackage is
// TestPlaceholderTokensMatchReviewPackage's own exact mirror for
// internal/domain/turn's three EPISTEMIC_OUTCOME_TOOL_* placeholders
// (turn/epistemicpreamble.go, Step 61/§20.2) -- added by F1 (adversarial
// review): these three were the verified omission (placeholderTokens'
// own doc comment, prompt.go) that let an attacker-controlled filename
// carrying a literal "{{EPISTEMIC_OUTCOME_TOOL_BEARER}}" survive
// sanitizeUntrustedField and later get expanded into the live sandbox
// bearer by sandbox-agent's own unconditional substitution. Same
// reasoning as the review test above: if internal/domain/turn ever
// renames or rotates one of these three without a matching update here,
// this fails CI immediately rather than silently reopening that gap.
func TestPlaceholderTokensMatchTurnPackage(t *testing.T) {
	t.Parallel()

	for _, tok := range []string{
		turn.EpistemicOutcomeToolURLPlaceholder,
		turn.EpistemicOutcomeToolBearerPlaceholder,
		turn.EpistemicOutcomeToolGenPlaceholder,
	} {
		if !containsToken(placeholderTokens, tok) {
			t.Errorf("placeholderTokens = %v, want it to contain turn's own real placeholder %q", placeholderTokens, tok)
		}
	}
}

// TestPlaceholderTokensExactCount pins placeholderTokens' own total size --
// this package's own three, plus review's own three, plus turn's own three,
// no more no less (F1, adversarial review: bumped 6 -> 9 when turn's three
// EPISTEMIC_OUTCOME_TOOL_* literals were registered). A future family that
// grows this list without a corresponding drift-matcher test above (or
// without the general cross-domain-package scan,
// placeholderdrift_internal_test.go) fails here first, forcing a deliberate
// update to this exact number rather than an unnoticed size change.
func TestPlaceholderTokensExactCount(t *testing.T) {
	t.Parallel()

	// This package's own three, for completeness -- trivially true by
	// construction today, but guards against a future refactor of
	// placeholderTokens' own literal accidentally dropping one.
	for _, tok := range []string{BaseURLPlaceholder, BearerPlaceholder, GenPlaceholder} {
		if !containsToken(placeholderTokens, tok) {
			t.Errorf("placeholderTokens = %v, want it to contain this package's own %q", placeholderTokens, tok)
		}
	}

	if len(placeholderTokens) != 9 {
		t.Errorf("len(placeholderTokens) = %d, want exactly 9 (this package's own 3, plus review's own 3, plus turn's own 3, no more no less)", len(placeholderTokens))
	}
}

// containsToken reports whether want appears anywhere in tokens -- shared
// by every drift-matcher test in this file.
func containsToken(tokens []string, want string) bool {
	for _, got := range tokens {
		if got == want {
			return true
		}
	}
	return false
}
