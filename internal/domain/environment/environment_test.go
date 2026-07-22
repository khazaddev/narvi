package environment_test

import (
	"errors"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/environment"
)

// TestValidatePathScope is table-driven over every pattern shape §14.1's
// validation rule cares about: an empty overall slice, a nil overall
// slice, an empty-string pattern, ".." as a full path segment (both alone
// and buried in a longer pattern), ".." as part of a longer segment (e.g.
// "foo..bar") which must NOT be rejected, a syntactically invalid glob per
// path.Match, a valid leading-"/" pattern, and a valid nested pattern.
func TestValidatePathScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		in         []string
		wantErr    bool
		wantReason error
	}{
		{
			name: "nil slice is valid (unscoped)",
			in:   nil,
		},
		{
			name: "empty slice is valid (unscoped)",
			in:   []string{},
		},
		{
			name:       "empty-string pattern is rejected",
			in:         []string{""},
			wantErr:    true,
			wantReason: environment.ErrEmptyPattern,
		},
		{
			name:       "bare .. is rejected",
			in:         []string{".."},
			wantErr:    true,
			wantReason: environment.ErrPathTraversal,
		},
		{
			name:       ".. as a leading segment is rejected",
			in:         []string{"../etc/passwd"},
			wantErr:    true,
			wantReason: environment.ErrPathTraversal,
		},
		{
			name:       ".. as a buried segment is rejected",
			in:         []string{"apps/../etc"},
			wantErr:    true,
			wantReason: environment.ErrPathTraversal,
		},
		{
			name: "foo..bar is NOT rejected (not a full .. segment)",
			in:   []string{"apps/foo..bar"},
		},
		{
			name:       "!.. (negation-prefixed traversal) is rejected",
			in:         []string{"!.."},
			wantErr:    true,
			wantReason: environment.ErrPathTraversal,
		},
		{
			name:       "!../etc (negation-prefixed traversal with a trailing segment) is rejected",
			in:         []string{"!../etc"},
			wantErr:    true,
			wantReason: environment.ErrPathTraversal,
		},
		{
			name:       "syntactically invalid glob is rejected",
			in:         []string{"apps/foo["},
			wantErr:    true,
			wantReason: environment.ErrInvalidGlobSyntax,
		},
		{
			name: "valid leading-/ pattern is allowed",
			in:   []string{"/apps/web/*"},
		},
		{
			name: "valid nested pattern is allowed",
			in:   []string{"apps/web/src/**"},
		},
		{
			name: "multiple valid patterns are allowed",
			in:   []string{"/apps/web/*", "packages/contracts/*"},
		},
		{
			name:       "first invalid pattern in a multi-entry slice is reported",
			in:         []string{"/apps/web/*", ""},
			wantErr:    true,
			wantReason: environment.ErrEmptyPattern,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := environment.ValidatePathScope(tc.in)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("ValidatePathScope(%v) = %v, want nil", tc.in, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidatePathScope(%v) = nil, want error wrapping %v", tc.in, tc.wantReason)
			}
			if !errors.Is(err, tc.wantReason) {
				t.Errorf("ValidatePathScope(%v) = %v, want error wrapping %v", tc.in, err, tc.wantReason)
			}
			var globErr *environment.InvalidGlobError
			if !errors.As(err, &globErr) {
				t.Fatalf("ValidatePathScope(%v) error is not *InvalidGlobError: %v", tc.in, err)
			}
			if globErr.Error() == "" {
				t.Error("InvalidGlobError.Error() is empty")
			}
		})
	}
}

// TestIsScoped covers the three shapes IsScoped's single len() check
// distinguishes: nil, empty, and non-empty.
func TestIsScoped(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   environment.Environment
		want bool
	}{
		{
			name: "nil PathScope is unscoped",
			in:   environment.Environment{PathScope: nil},
			want: false,
		},
		{
			name: "empty PathScope is unscoped",
			in:   environment.Environment{PathScope: []string{}},
			want: false,
		},
		{
			name: "non-empty PathScope is scoped",
			in:   environment.Environment{PathScope: []string{"/apps/web/*"}},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := environment.IsScoped(tc.in); got != tc.want {
				t.Errorf("IsScoped(%+v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestSparseCheckoutPatterns covers both branches: an unscoped Environment
// must return nil (so a caller can distinguish "skip sparse-checkout"
// from "run it with an empty list"), and a scoped one must return its
// patterns verbatim.
func TestSparseCheckoutPatterns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   environment.Environment
		want []string
	}{
		{
			name: "unscoped (nil PathScope) returns nil",
			in:   environment.Environment{PathScope: nil},
			want: nil,
		},
		{
			name: "unscoped (empty PathScope) returns nil",
			in:   environment.Environment{PathScope: []string{}},
			want: nil,
		},
		{
			name: "scoped returns patterns verbatim",
			in:   environment.Environment{PathScope: []string{"/apps/web/*", "packages/contracts/*"}},
			want: []string{"/apps/web/*", "packages/contracts/*"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := environment.SparseCheckoutPatterns(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("SparseCheckoutPatterns(%+v) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("SparseCheckoutPatterns(%+v)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestRequiresProvenanceTag mirrors TestIsScoped's cases: the two
// functions compute the same value, so their test cases and expectations
// are the same shape by design (see doc.go/environment.go for why the
// names are nonetheless kept distinct).
func TestRequiresProvenanceTag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   environment.Environment
		want bool
	}{
		{
			name: "nil PathScope does not require a provenance tag",
			in:   environment.Environment{PathScope: nil},
			want: false,
		},
		{
			name: "empty PathScope does not require a provenance tag",
			in:   environment.Environment{PathScope: []string{}},
			want: false,
		},
		{
			name: "non-empty PathScope requires a provenance tag",
			in:   environment.Environment{PathScope: []string{"/apps/web/*"}},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := environment.RequiresProvenanceTag(tc.in); got != tc.want {
				t.Errorf("RequiresProvenanceTag(%+v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestValidateContractsPath is table-driven over every shape this audit
// remediation's own validation rule cares about: an empty string, ".." as
// a full path segment (both alone and buried in a longer path), ".." as
// part of a longer segment (must NOT be rejected, mirroring
// TestValidatePathScope's own identical "foo..bar" case), a "?" or "#"
// (the exact two characters that could otherwise rewrite/truncate the
// outbound githubapi request this value is later interpolated into), and a
// valid nested path.
func TestValidateContractsPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		in         string
		wantErr    bool
		wantReason error
	}{
		{
			name:       "empty string is rejected",
			in:         "",
			wantErr:    true,
			wantReason: environment.ErrContractsPathEmpty,
		},
		{
			name:       "bare .. is rejected",
			in:         "..",
			wantErr:    true,
			wantReason: environment.ErrContractsPathTraversal,
		},
		{
			name:       ".. as a leading segment is rejected",
			in:         "../etc/passwd",
			wantErr:    true,
			wantReason: environment.ErrContractsPathTraversal,
		},
		{
			name:       ".. as a buried segment is rejected",
			in:         "contracts/../etc",
			wantErr:    true,
			wantReason: environment.ErrContractsPathTraversal,
		},
		{
			name: "foo..bar is NOT rejected (not a full .. segment)",
			in:   "contracts/foo..bar",
		},
		{
			name:       "a ? is rejected (query-injection shape)",
			in:         "contracts/api?ref=attacker",
			wantErr:    true,
			wantReason: environment.ErrContractsPathInvalidChars,
		},
		{
			name:       "a # is rejected (fragment-truncation shape)",
			in:         "contracts/api#evil",
			wantErr:    true,
			wantReason: environment.ErrContractsPathInvalidChars,
		},
		{
			name: "a valid nested path is allowed",
			in:   "services/mock-api/contracts",
		},
		{
			name: "the plain default path is allowed",
			in:   "contracts/api",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := environment.ValidateContractsPath(tc.in)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("ValidateContractsPath(%q) = %v, want nil", tc.in, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateContractsPath(%q) = nil, want error wrapping %v", tc.in, tc.wantReason)
			}
			if !errors.Is(err, tc.wantReason) {
				t.Errorf("ValidateContractsPath(%q) = %v, want error wrapping %v", tc.in, err, tc.wantReason)
			}
			var pathErr *environment.InvalidContractsPathError
			if !errors.As(err, &pathErr) {
				t.Fatalf("ValidateContractsPath(%q) error is not *InvalidContractsPathError: %v", tc.in, err)
			}
			if pathErr.Error() == "" {
				t.Error("InvalidContractsPathError.Error() is empty")
			}
		})
	}
}
