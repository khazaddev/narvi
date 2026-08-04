package automation_test

import (
	"errors"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/automation"
)

func TestValidateTargets(t *testing.T) {
	oneTarget := []automation.Target{{Name: "repo", URL: "https://github.com/org/repo"}}

	tenTargets := make([]automation.Target, automation.MaxFanOutTargets)
	for i := range tenTargets {
		tenTargets[i] = automation.Target{Name: "repo", URL: "https://github.com/org/repo"}
	}

	elevenTargets := append(append([]automation.Target{}, tenTargets...), automation.Target{Name: "extra", URL: "https://github.com/org/extra"})

	tests := []struct {
		name    string
		targets []automation.Target
		wantErr error
	}{
		{"nil targets", nil, automation.ErrNoTargets},
		{"empty targets", []automation.Target{}, automation.ErrNoTargets},
		{"one target", oneTarget, nil},
		{"exactly the max", tenTargets, nil},
		{"one over the max", elevenTargets, automation.ErrTooManyTargets},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := automation.ValidateTargets(tt.targets)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("got %v, want %v", err, tt.wantErr)
			}
		})
	}
}
