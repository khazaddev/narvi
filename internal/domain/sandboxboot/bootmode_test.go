package sandboxboot_test

import (
	"errors"
	"testing"

	"github.com/narvidev/narvi/internal/domain/sandboxboot"
)

func TestParseBootMode_Valid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw  string
		want sandboxboot.BootMode
	}{
		{"build", sandboxboot.BootModeBuild},
		{"fresh", sandboxboot.BootModeFresh},
		{"repo_image", sandboxboot.BootModeRepoImage},
		{"snapshot_restore", sandboxboot.BootModeSnapshotRestore},
	}

	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()

			got, err := sandboxboot.ParseBootMode(tc.raw)
			if err != nil {
				t.Fatalf("ParseBootMode(%q) error = %v, want nil", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("ParseBootMode(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParseBootMode_Invalid(t *testing.T) {
	t.Parallel()

	tests := []string{
		"",                 // unset -- §6.4 gives no default, must fail fast
		"Build",            // wrong case
		"BUILD",            // wrong case
		"fresh ",           // trailing space, no normalizing beyond exact match
		"repoimage",        // missing underscore
		"snapshot-restore", // wrong separator
		"garbage",
	}

	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			got, err := sandboxboot.ParseBootMode(raw)
			if err == nil {
				t.Fatalf("ParseBootMode(%q) error = nil, want *InvalidBootModeError", raw)
			}
			if got != "" {
				t.Errorf("ParseBootMode(%q) = %q, want zero value on error", raw, got)
			}

			var invErr *sandboxboot.InvalidBootModeError
			if !errors.As(err, &invErr) {
				t.Fatalf("ParseBootMode(%q) error = %v (%T), want *InvalidBootModeError", raw, err, err)
			}
			if invErr.Value != raw {
				t.Errorf("InvalidBootModeError.Value = %q, want %q", invErr.Value, raw)
			}
			if invErr.Error() == "" {
				t.Errorf("InvalidBootModeError.Error() = %q, want a non-empty message", invErr.Error())
			}
		})
	}
}
