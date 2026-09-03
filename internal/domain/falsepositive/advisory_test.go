package falsepositive_test

import (
	"strings"
	"testing"

	"github.com/narvidev/narvi/internal/domain/falsepositive"
)

func TestRenderAdvisoryBlock_Empty(t *testing.T) {
	t.Parallel()

	got := falsepositive.RenderAdvisoryBlock(nil)
	if got != "" {
		t.Fatalf("RenderAdvisoryBlock(nil) = %q, want empty string", got)
	}

	got = falsepositive.RenderAdvisoryBlock([]falsepositive.Pattern{})
	if got != "" {
		t.Fatalf("RenderAdvisoryBlock([]) = %q, want empty string", got)
	}
}

func TestRenderAdvisoryBlock_ContainsEveryPatternAndAdvisoryWording(t *testing.T) {
	t.Parallel()

	patterns := []falsepositive.Pattern{
		{Reason: "unchecked error on this specific logger call is intentional"},
		{Reason: "TODO comments in this repo are tracked separately, not findings"},
	}

	got := falsepositive.RenderAdvisoryBlock(patterns)

	for _, p := range patterns {
		if !strings.Contains(got, p.Reason) {
			t.Errorf("RenderAdvisoryBlock output missing reason %q; got:\n%s", p.Reason, got)
		}
	}

	// §22.3's own explicit "advisory, never a filter" instruction must be
	// present in the rendered text -- this is what tells the MODEL to
	// weigh, not obey.
	for _, want := range []string{"weigh", "verify independently", "do not skip"} {
		if !strings.Contains(strings.ToLower(got), strings.ToLower(want)) {
			t.Errorf("RenderAdvisoryBlock output missing advisory wording %q; got:\n%s", want, got)
		}
	}

	// Delimited, exactly like every other untrusted/contextual block this
	// codebase renders (§5.2) -- both open and close tags present, close
	// after open.
	openIdx := strings.Index(got, "<learned_false_positive_patterns>")
	closeIdx := strings.Index(got, "</learned_false_positive_patterns>")
	if openIdx == -1 || closeIdx == -1 || closeIdx < openIdx {
		t.Fatalf("RenderAdvisoryBlock output not properly delimited; got:\n%s", got)
	}
}

// TestRenderAdvisoryBlock_TakesNoFindingsParameter is a structural,
// compile-time-shaped assertion, not a runtime one: RenderAdvisoryBlock's
// signature is func([]Pattern) string. There is no findings/verdict
// parameter for this test to pass a value into in the first place -- which
// is exactly the "structurally incapable of acting as a filter" property
// this package's own doc comment claims: a function that never receives
// the thing a filter would need to inspect cannot filter it. This test
// exists as an explicit, named pin on that shape so a future signature
// change (e.g. accepting findings to "helpfully" auto-suppress a match)
// cannot land without a reviewer seeing this test's own name and intent.
func TestRenderAdvisoryBlock_TakesNoFindingsParameter(t *testing.T) {
	t.Parallel()

	// This line only compiles because RenderAdvisoryBlock accepts exactly
	// one argument, []Pattern -- if a future change widened the signature
	// to also accept findings, this call would need updating, which is
	// the point.
	_ = falsepositive.RenderAdvisoryBlock([]falsepositive.Pattern{{Reason: "x"}})
}
