package reviewpost_test

import (
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/reviewpost"
)

// TestRenderAutofixBody_ProposedBeforeCollapsedOriginal proves §26.2's own
// central shape: the proposed body renders FIRST, visible without
// expanding anything, and the original renders SECOND, inside a collapsed
// <details> block -- "preserving the original in a collapsed block".
func TestRenderAutofixBody_ProposedBeforeCollapsedOriginal(t *testing.T) {
	t.Parallel()

	original := "Fixes a typo."
	proposed := "Rewrites the auth token refresh path to retry on transient network failures."

	got := reviewpost.RenderAutofixBody(original, proposed)

	if !strings.Contains(got, proposed) {
		t.Errorf("RenderAutofixBody() missing the proposed body in:\n%s", got)
	}
	if !strings.Contains(got, original) {
		t.Errorf("RenderAutofixBody() missing the original body in:\n%s", got)
	}

	proposedIdx := strings.Index(got, proposed)
	detailsIdx := strings.Index(got, "<details>")
	originalIdx := strings.Index(got, original)
	closeIdx := strings.Index(got, "</details>")
	if proposedIdx == -1 || detailsIdx == -1 || originalIdx == -1 || closeIdx == -1 {
		t.Fatalf("expected all four markers present, got:\n%s", got)
	}
	if proposedIdx >= detailsIdx || detailsIdx >= originalIdx || originalIdx >= closeIdx {
		t.Errorf("expected order [proposed, <details>, original, </details>], got indices %d, %d, %d, %d in:\n%s",
			proposedIdx, detailsIdx, originalIdx, closeIdx, got)
	}
	if !strings.Contains(got, "Original description") {
		t.Errorf("RenderAutofixBody() missing an \"Original description\" summary label in:\n%s", got)
	}
}

// TestRenderAutofixBody_BlankOriginalRendersPlaceholder proves a PR opened
// with no description at all (a real, if uncommon, case) still renders a
// valid, non-empty collapsed block -- never a dangling empty <details>
// section.
func TestRenderAutofixBody_BlankOriginalRendersPlaceholder(t *testing.T) {
	t.Parallel()

	got := reviewpost.RenderAutofixBody("", "A real proposed description.")

	if !strings.Contains(got, "no original description") {
		t.Errorf("RenderAutofixBody() missing an honest placeholder for a blank original body in:\n%s", got)
	}
}

// TestRenderAutofixBody_WhitespaceOnlyOriginalRendersPlaceholder proves
// the SAME placeholder fires for a whitespace-only original body, not
// only a literally-empty one.
func TestRenderAutofixBody_WhitespaceOnlyOriginalRendersPlaceholder(t *testing.T) {
	t.Parallel()

	got := reviewpost.RenderAutofixBody("   \n\t  ", "A real proposed description.")

	if !strings.Contains(got, "no original description") {
		t.Errorf("RenderAutofixBody() missing an honest placeholder for a whitespace-only original body in:\n%s", got)
	}
}

// TestRenderAutofixBody_NeverMentionsTitle documents §26.2's own explicit
// "the title is never rewritten automatically" rule at this function's
// own level: nothing in its own output vocabulary references a title at
// all -- this function's signature itself has no title parameter, so
// there is structurally nothing for a caller to even pass one through.
func TestRenderAutofixBody_NeverMentionsTitle(t *testing.T) {
	t.Parallel()

	got := reviewpost.RenderAutofixBody("Original.", "Proposed.")
	if strings.Contains(strings.ToLower(got), "title") {
		t.Errorf("RenderAutofixBody() output unexpectedly mentions \"title\" -- this function must never touch or reference the PR title:\n%s", got)
	}
}
