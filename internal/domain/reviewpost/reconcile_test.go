package reviewpost_test

import (
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/reviewpost"
)

func TestRenderAlreadyAnsweredFacts_EmptyReturnsEmptyString(t *testing.T) {
	if got := reviewpost.RenderAlreadyAnsweredFacts(nil, nil); got != "" {
		t.Errorf("RenderAlreadyAnsweredFacts(nil, nil) = %q, want empty string", got)
	}
	if got := reviewpost.RenderAlreadyAnsweredFacts([]reviewpost.ReconciledFinding{}, nil); got != "" {
		t.Errorf("RenderAlreadyAnsweredFacts([], nil) = %q, want empty string", got)
	}
	// An empty-but-non-nil findings slice with a real changedPaths list
	// still short-circuits on the same len(findings) == 0 check -- the
	// retirement machinery never even runs.
	if got := reviewpost.RenderAlreadyAnsweredFacts([]reviewpost.ReconciledFinding{}, []string{"a.go"}); got != "" {
		t.Errorf("RenderAlreadyAnsweredFacts([], [a.go]) = %q, want empty string", got)
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

	got := reviewpost.RenderAlreadyAnsweredFacts(findings, nil)

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

	got := reviewpost.RenderAlreadyAnsweredFacts(findings, nil)
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
	}, nil)

	open := "<already_answered_findings>"
	closeTag := "</already_answered_findings>"
	openIdx := strings.Index(got, open)
	closeIdx := strings.Index(got, closeTag)
	if openIdx == -1 || closeIdx == -1 || closeIdx < openIdx {
		t.Fatalf("RenderAlreadyAnsweredFacts() not properly delimited:\n%s", got)
	}
}

// The following table-driven test is §22.1.2's own "determinable fact"
// refinement, now shipped: a finding whose FilePath is not among the
// current diff's own changedPaths is marked RETIRED -- never dropped
// from the rendered block (§22.3's advisory-never-a-filter posture, and
// this function's own doc comment: "note-but-don't-block").
func TestRenderAlreadyAnsweredFacts_Retirement(t *testing.T) {
	tests := []struct {
		name         string
		filePath     string
		changedPaths []string
		wantRetired  bool
	}{
		{
			name:         "file still in diff is not retired",
			filePath:     "internal/foo/bar.go",
			changedPaths: []string{"internal/foo/bar.go", "internal/other.go"},
			wantRetired:  false,
		},
		{
			name:         "file no longer in diff is retired",
			filePath:     "internal/foo/bar.go",
			changedPaths: []string{"internal/other.go"},
			wantRetired:  true,
		},
		{
			name:         "path normalization: ./ prefix still matches",
			filePath:     "./internal/foo/bar.go",
			changedPaths: []string{"internal/foo/bar.go"},
			wantRetired:  false,
		},
		{
			name:         "nil changedPaths (no diff data) never retires -- fail-safe",
			filePath:     "internal/foo/bar.go",
			changedPaths: nil,
			wantRetired:  false,
		},
		{
			name:         "empty-but-non-nil changedPaths is treated identically to nil -- fail-safe",
			filePath:     "internal/foo/bar.go",
			changedPaths: []string{},
			wantRetired:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			findings := []reviewpost.ReconciledFinding{
				{
					IdentityHash: "abc123",
					FilePath:     tt.filePath,
					Description:  "some finding",
					Status:       reviewpost.FindingStatusOpen,
				},
			}
			got := reviewpost.RenderAlreadyAnsweredFacts(findings, tt.changedPaths)

			// "RETIRED:" (with the colon) is the per-finding annotation's
			// own marker -- distinct from the block's fixed header
			// sentence, which also mentions the word "RETIRED" but never
			// followed by a colon, so a plain Contains(got, "RETIRED")
			// would false-positive on every render regardless of whether
			// any finding actually retired.
			gotRetired := strings.Contains(got, "RETIRED:")
			if gotRetired != tt.wantRetired {
				t.Errorf("RenderAlreadyAnsweredFacts() RETIRED marker present = %v, want %v, got:\n%s", gotRetired, tt.wantRetired, got)
			}
			// A retired finding is still rendered in full -- never
			// silently dropped from the block (the whole point of
			// "note-but-don't-block").
			if !strings.Contains(got, "some finding") {
				t.Errorf("RenderAlreadyAnsweredFacts() dropped the finding entirely instead of noting retirement:\n%s", got)
			}
		})
	}
}

// TestRenderAlreadyAnsweredFacts_RetirementNeverAppliesJudgement is a
// second table-driven test making the SAME point §22.1.2 draws
// explicitly: retirement is decided purely from FilePath membership in
// changedPaths, never from any similarity between one finding's own
// Description and another's, or between two findings on the same file.
// Two findings that share a file but differ in every other respect are
// retired/not-retired identically, based solely on that one file's own
// diff membership -- there is no content comparison anywhere in this
// path.
func TestRenderAlreadyAnsweredFacts_RetirementNeverAppliesJudgement(t *testing.T) {
	findings := []reviewpost.ReconciledFinding{
		{IdentityHash: "a", FilePath: "internal/foo.go", Description: "First unrelated finding.", Status: reviewpost.FindingStatusOpen},
		{IdentityHash: "b", FilePath: "internal/foo.go", Description: "Second, totally different finding.", Status: reviewpost.FindingStatusRebutted},
	}

	// internal/foo.go left the diff entirely -- both findings on it
	// retire together, regardless of their differing descriptions.
	got := reviewpost.RenderAlreadyAnsweredFacts(findings, []string{"internal/bar.go"})

	if strings.Count(got, "RETIRED:") != 2 {
		t.Errorf("RenderAlreadyAnsweredFacts() = %q, want exactly 2 RETIRED markers (one per finding on the out-of-diff file)", got)
	}
}
