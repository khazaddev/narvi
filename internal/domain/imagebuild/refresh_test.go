package imagebuild_test

import (
	"testing"

	"github.com/narvidev/narvi/internal/domain/imagebuild"
)

func TestNeedsRefresh(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		built   map[string]string
		current map[string]string
		want    bool
	}{
		{
			name:    "identical SHAs, no refresh needed",
			built:   map[string]string{"repo1": "sha-a", "repo2": "sha-b"},
			current: map[string]string{"repo1": "sha-a", "repo2": "sha-b"},
			want:    false,
		},
		{
			name:    "one repo's tip moved",
			built:   map[string]string{"repo1": "sha-a", "repo2": "sha-b"},
			current: map[string]string{"repo1": "sha-a", "repo2": "sha-b-NEW"},
			want:    true,
		},
		{
			name:    "every repo moved",
			built:   map[string]string{"repo1": "sha-a"},
			current: map[string]string{"repo1": "sha-a-NEW"},
			want:    true,
		},
		{
			name:    "current repo has no built entry at all -- safe default, treat as stale",
			built:   map[string]string{},
			current: map[string]string{"repo1": "sha-a"},
			want:    true,
		},
		{
			name:    "both empty (base-only fingerprint, unreachable via ListReadyImageBuilds but defensively pure)",
			built:   map[string]string{},
			current: map[string]string{},
			want:    false,
		},
		{
			name:    "built has an extra repo not present in current -- irrelevant, never inspected",
			built:   map[string]string{"repo1": "sha-a", "repo-extra": "sha-x"},
			current: map[string]string{"repo1": "sha-a"},
			want:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := imagebuild.NeedsRefresh(tc.built, tc.current); got != tc.want {
				t.Errorf("NeedsRefresh(%v, %v) = %v, want %v", tc.built, tc.current, got, tc.want)
			}
		})
	}
}
