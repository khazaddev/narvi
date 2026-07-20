package supervisor

import (
	"strings"
	"testing"
)

// hasKey reports whether env (in os.Environ() "KEY=VALUE" form) contains an
// entry whose key exactly equals name.
func hasKey(env []string, name string) bool {
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if key == name {
			return true
		}
	}
	return false
}

// These tests use t.Setenv, which the testing package forbids combining
// with t.Parallel() (env vars are process-global) -- so none of them call
// t.Parallel.

func TestEnvWithout(t *testing.T) {
	t.Setenv("NARVI_EXCLUDE_ME", "secret")
	t.Setenv("NARVI_EXCLUDE_ME_TOO", "also-secret")
	t.Setenv("NARVI_KEEP_ME", "kept")

	tests := []struct {
		name    string
		exclude []string
		want    map[string]bool // key -> wantPresent
	}{
		{
			name:    "single name excluded",
			exclude: []string{"NARVI_EXCLUDE_ME"},
			want: map[string]bool{
				"NARVI_EXCLUDE_ME":     false,
				"NARVI_EXCLUDE_ME_TOO": true,
				"NARVI_KEEP_ME":        true,
				"PATH":                 true,
			},
		},
		{
			name:    "variadic multi-name exclusion",
			exclude: []string{"NARVI_EXCLUDE_ME", "NARVI_EXCLUDE_ME_TOO"},
			want: map[string]bool{
				"NARVI_EXCLUDE_ME":     false,
				"NARVI_EXCLUDE_ME_TOO": false,
				"NARVI_KEEP_ME":        true,
				"PATH":                 true,
			},
		},
		{
			name:    "a name matching nothing is a safe no-op",
			exclude: []string{"NARVI_DOES_NOT_EXIST"},
			want: map[string]bool{
				"NARVI_EXCLUDE_ME":     true,
				"NARVI_EXCLUDE_ME_TOO": true,
				"NARVI_KEEP_ME":        true,
				"PATH":                 true,
			},
		},
		{
			name:    "no names at all excludes nothing",
			exclude: nil,
			want: map[string]bool{
				"NARVI_EXCLUDE_ME":     true,
				"NARVI_EXCLUDE_ME_TOO": true,
				"NARVI_KEEP_ME":        true,
				"PATH":                 true,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EnvWithout(tc.exclude...)
			for key, wantPresent := range tc.want {
				if gotPresent := hasKey(got, key); gotPresent != wantPresent {
					t.Errorf("EnvWithout(%v): key %q present = %v, want %v", tc.exclude, key, gotPresent, wantPresent)
				}
			}
		})
	}
}

// TestEnvWithout_PreservesRelativeOrder proves EnvWithout only removes
// matching entries, never reorders survivors.
func TestEnvWithout_PreservesRelativeOrder(t *testing.T) {
	t.Setenv("NARVI_EXCLUDE_ME", "secret")

	before := EnvWithout() // no exclusion -- effectively os.Environ() copied
	after := EnvWithout("NARVI_EXCLUDE_ME")

	var filteredBefore []string
	for _, entry := range before {
		key, _, _ := strings.Cut(entry, "=")
		if key != "NARVI_EXCLUDE_ME" {
			filteredBefore = append(filteredBefore, entry)
		}
	}

	if len(filteredBefore) != len(after) {
		t.Fatalf("len(after) = %d, want %d", len(after), len(filteredBefore))
	}
	for i := range filteredBefore {
		if filteredBefore[i] != after[i] {
			t.Errorf("order mismatch at index %d: got %q, want %q", i, after[i], filteredBefore[i])
		}
	}
}
