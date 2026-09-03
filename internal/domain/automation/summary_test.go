package automation_test

import (
	"testing"

	"github.com/narvidev/narvi/internal/domain/automation"
)

func TestBuildArtifactSummary(t *testing.T) {
	tests := []struct {
		name              string
		totalRuns         int
		failedRuns        int
		failedTargetNames []string
		want              string
	}{
		{"single target succeeded", 1, 0, nil, "Succeeded: 1/1 target."},
		{"all succeeded", 3, 0, nil, "Succeeded: 3/3 targets."},
		{"one of three failed, named", 3, 1, []string{"repo-b"}, "Failed: 1/3 targets failed (repo-b); 2 succeeded."},
		{"two of three failed, named", 3, 2, []string{"repo-a", "repo-b"}, "Failed: 2/3 targets failed (repo-a, repo-b); 1 succeeded."},
		{"all failed", 2, 2, []string{"repo-a", "repo-b"}, "Failed: 2/2 targets failed (repo-a, repo-b); 0 succeeded."},
		{"failed with no target names available", 1, 1, nil, "Failed: 1/1 targets failed (unknown target); 0 succeeded."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := automation.BuildArtifactSummary(tt.totalRuns, tt.failedRuns, tt.failedTargetNames)
			if got != tt.want {
				t.Fatalf("BuildArtifactSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}
