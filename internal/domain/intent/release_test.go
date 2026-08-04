package intent_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/intent"
)

func TestMatchesReleaseBranch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		branch  string
		pattern string
		want    bool
	}{
		{"matches release/*", "release/2.4", "release/*", true},
		{"matches release/* with nested-looking name", "release/hotfix-2.4.1", "release/*", true},
		{"does not match a non-release branch", "feature/foo", "release/*", false},
		{"does not match a bare release branch with no suffix segment", "release", "release/*", false},
		{"empty branch never matches", "", "release/*", false},
		{"empty pattern never matches", "release/2.4", "", false},
		{"both empty never matches", "", "", false},
		{"exact non-glob pattern matches exactly", "main", "main", true},
		{"exact non-glob pattern does not match a different branch", "develop", "main", false},
		{"malformed glob pattern fails conservative (no match, no panic)", "release/2.4", "release/[", false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := intent.MatchesReleaseBranch(tc.branch, tc.pattern); got != tc.want {
				t.Errorf("MatchesReleaseBranch(%q, %q) = %v, want %v", tc.branch, tc.pattern, got, tc.want)
			}
		})
	}
}

func TestHasReleaseLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		labels       []string
		releaseLabel string
		want         bool
	}{
		{"exact match present", []string{"bug", "release"}, "release", true},
		{"no match", []string{"bug", "enhancement"}, "release", false},
		{"empty labels", nil, "release", false},
		{"empty releaseLabel never matches, even against an identically-empty label", []string{""}, "", false},
		{"case-sensitive: does not match a differently-cased label", []string{"Release"}, "release", false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := intent.HasReleaseLabel(tc.labels, tc.releaseLabel); got != tc.want {
				t.Errorf("HasReleaseLabel(%v, %q) = %v, want %v", tc.labels, tc.releaseLabel, got, tc.want)
			}
		})
	}
}

// TestDetectRelease_MatchesSpecOR proves §15.1's own detection rule is a
// genuine OR across head branch, base branch, and label -- any single
// signal is sufficient.
func TestDetectRelease_MatchesSpecOR(t *testing.T) {
	t.Parallel()

	const pattern = "release/*"
	const label = "release"

	tests := []struct {
		name       string
		headBranch string
		baseBranch string
		labels     []string
		want       bool
	}{
		{"head branch matches the pattern", "release/2.4", "main", nil, true},
		{"base branch matches the pattern (e.g. a develop->release/* promotion target)", "develop", "release/2.4", nil, true},
		{"label present", "develop", "main", []string{"release"}, true},
		{"none of the three: not a release PR", "feature/foo", "main", []string{"bug"}, false},
		{"ordinary PR, no signals at all", "feature/foo", "main", nil, false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := intent.DetectRelease(tc.headBranch, tc.baseBranch, tc.labels, pattern, label)
			if got != tc.want {
				t.Errorf("DetectRelease(%q, %q, %v, %q, %q) = %v, want %v",
					tc.headBranch, tc.baseBranch, tc.labels, pattern, label, got, tc.want)
			}
		})
	}
}

// TestDetectRelease_EmptyConfigNeverFires proves an unconfigured
// (empty-string) branchPattern/releaseLabel never causes a false
// positive, mirroring parsePullRequestLabeled's own "empty configured
// label... this lane never fires" precedent.
func TestDetectRelease_EmptyConfigNeverFires(t *testing.T) {
	t.Parallel()

	if got := intent.DetectRelease("release/2.4", "main", []string{"release"}, "", ""); got {
		t.Errorf("DetectRelease with empty pattern/label = true, want false (must never wildcard-match)")
	}
}
