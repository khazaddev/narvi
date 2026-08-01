package sentinelfix_test

import (
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/sentinelfix"
)

func TestIsTestOrDocPath_TableDriven(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"internal/foo/bar_test.go", true},
		{"internal/foo/bar.go", false},
		{"docs/README.md", true},
		{"README.md", true},
		{"CHANGES.mdx", true},
		{"internal/foo/testdata/fixture.json", true},
		{"internal/testdata/x/y.go", true},
		{"internal/foo/bar.txt", false},
		{"", false},
		{"internal/foo/docs/notes.txt", true},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := sentinelfix.IsTestOrDocPath(tt.path); got != tt.want {
				t.Errorf("IsTestOrDocPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestEvaluateMergeGate_TableDriven(t *testing.T) {
	tests := []struct {
		name            string
		changedFiles    []string
		ciGreen         bool
		cherryPickClean bool
		toggleEnabled   bool
		wantAllowed     bool
		wantReasonHas   string
	}{
		{
			name:            "all four hold: allowed",
			changedFiles:    []string{"internal/foo/bar_test.go", "docs/README.md"},
			ciGreen:         true,
			cherryPickClean: true,
			toggleEnabled:   true,
			wantAllowed:     true,
		},
		{
			name:            "toggle off: denied first, even if everything else holds",
			changedFiles:    []string{"internal/foo/bar_test.go"},
			ciGreen:         true,
			cherryPickClean: true,
			toggleEnabled:   false,
			wantAllowed:     false,
			wantReasonHas:   "toggle",
		},
		{
			name:            "CI red: denied",
			changedFiles:    []string{"internal/foo/bar_test.go"},
			ciGreen:         false,
			cherryPickClean: true,
			toggleEnabled:   true,
			wantAllowed:     false,
			wantReasonHas:   "CI",
		},
		{
			name:            "cherry-pick conflict: denied",
			changedFiles:    []string{"internal/foo/bar_test.go"},
			ciGreen:         true,
			cherryPickClean: false,
			toggleEnabled:   true,
			wantAllowed:     false,
			wantReasonHas:   "cherry-pick",
		},
		{
			name:            "non-test/doc file touched: denied",
			changedFiles:    []string{"internal/foo/bar_test.go", "internal/foo/bar.go"},
			ciGreen:         true,
			cherryPickClean: true,
			toggleEnabled:   true,
			wantAllowed:     false,
			wantReasonHas:   "internal/foo/bar.go",
		},
		{
			name:            "empty changed files list: allowed (nothing to violate scope)",
			changedFiles:    nil,
			ciGreen:         true,
			cherryPickClean: true,
			toggleEnabled:   true,
			wantAllowed:     true,
		},
		{
			name:            "every check failing: denied with the FIRST reason (toggle), deterministic order",
			changedFiles:    []string{"internal/foo/bar.go"},
			ciGreen:         false,
			cherryPickClean: false,
			toggleEnabled:   false,
			wantAllowed:     false,
			wantReasonHas:   "toggle",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sentinelfix.EvaluateMergeGate(tt.changedFiles, tt.ciGreen, tt.cherryPickClean, tt.toggleEnabled)
			if got.Allowed != tt.wantAllowed {
				t.Errorf("Allowed = %v, want %v (reason: %q)", got.Allowed, tt.wantAllowed, got.Reason)
			}
			if tt.wantReasonHas != "" && !strings.Contains(got.Reason, tt.wantReasonHas) {
				t.Errorf("Reason = %q, want it to mention %q", got.Reason, tt.wantReasonHas)
			}
		})
	}
}
