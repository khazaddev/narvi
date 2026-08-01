package reviewpost_test

import (
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/reviewpost"
)

func TestRenderAlreadyAnsweredFacts_EmptyReturnsEmptyString(t *testing.T) {
	if got := reviewpost.RenderAlreadyAnsweredFacts(nil); got != "" {
		t.Errorf("RenderAlreadyAnsweredFacts(nil) = %q, want empty string", got)
	}
	if got := reviewpost.RenderAlreadyAnsweredFacts([]reviewpost.ReconciledFinding{}); got != "" {
		t.Errorf("RenderAlreadyAnsweredFacts([]) = %q, want empty string", got)
	}
}

func TestRenderAlreadyAnsweredFacts_RendersEveryFindingAndRebuttal(t *testing.T) {
	rebuttal := "Intentional -- this path is unreachable by construction."
	findings := []reviewpost.ReconciledFinding{
		{
			IdentityHash: "abc123abc123abc123",
			SentinelKind: nil,
			FilePath:     "internal/foo/bar.go",
			Description:  "Missing error-path test.",
			Status:       reviewpost.FindingStatusOpen,
		},
		{
			IdentityHash: "def456def456def456",
			SentinelKind: nil,
			FilePath:     "internal/foo/baz.go",
			Description:  "Suspicious nil check.",
			Status:       reviewpost.FindingStatusRebutted,
			RebuttalText: &rebuttal,
		},
	}

	got := reviewpost.RenderAlreadyAnsweredFacts(findings)

	for _, want := range []string{
		"internal/foo/bar.go",
		"Missing error-path test.",
		"internal/foo/baz.go",
		"Suspicious nil check.",
		rebuttal,
		"already_answered_findings",
		string(reviewpost.FindingStatusOpen),
		string(reviewpost.FindingStatusRebutted),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderAlreadyAnsweredFacts() missing %q in:\n%s", want, got)
		}
	}
}

// TestRenderAlreadyAnsweredFacts_OpenFindingOmitsRebuttalText proves an
// open (not-yet-rebutted) finding never renders a dangling rebuttal
// mention -- RebuttalText is only ever non-nil for a genuinely rebutted
// finding, but this guards the render function's own behavior even if a
// caller passed one anyway for a non-rebutted status.
func TestRenderAlreadyAnsweredFacts_OpenFindingOmitsRebuttalText(t *testing.T) {
	leftover := "should not render"
	findings := []reviewpost.ReconciledFinding{
		{
			IdentityHash: "abc123",
			FilePath:     "a.go",
			Description:  "x",
			Status:       reviewpost.FindingStatusOpen,
			RebuttalText: &leftover,
		},
	}

	got := reviewpost.RenderAlreadyAnsweredFacts(findings)
	if strings.Contains(got, leftover) {
		t.Errorf("RenderAlreadyAnsweredFacts() rendered rebuttal text for a non-rebutted finding:\n%s", got)
	}
}

// TestRenderAlreadyAnsweredFacts_IsWrappedInADelimitedBlock mirrors
// internal/domain/review's own diff/stack delimiter discipline: the
// rendered block is wrapped in a fixed, recognizable open/close tag pair.
func TestRenderAlreadyAnsweredFacts_IsWrappedInADelimitedBlock(t *testing.T) {
	got := reviewpost.RenderAlreadyAnsweredFacts([]reviewpost.ReconciledFinding{
		{IdentityHash: "abc", FilePath: "a.go", Description: "x", Status: reviewpost.FindingStatusOpen},
	})

	open := "<already_answered_findings>"
	closeTag := "</already_answered_findings>"
	openIdx := strings.Index(got, open)
	closeIdx := strings.Index(got, closeTag)
	if openIdx == -1 || closeIdx == -1 || closeIdx < openIdx {
		t.Fatalf("RenderAlreadyAnsweredFacts() not properly delimited:\n%s", got)
	}
}
