package falsepositive_test

import (
	"errors"
	"testing"

	"github.com/narvidev/narvi/internal/domain/falsepositive"
)

func TestMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		text       string
		wantReason string
		wantOK     bool
	}{
		{"exact prefix with reason", "false positive: this is intentional dead code", "this is intentional dead code", true},
		{"case-insensitive prefix", "False Positive: intentional", "intentional", true},
		{"mixed-case prefix", "FaLsE PoSiTiVe: intentional", "intentional", true},
		{"leading/trailing whitespace around whole comment", "   false positive: intentional   ", "intentional", true},
		{"extra whitespace between prefix and reason", "false positive:    intentional", "intentional", true},
		{"empty reason after prefix", "false positive:", "", true},
		{"empty reason after prefix with trailing space", "false positive:   ", "", true},
		{"not a command at all", "this looks like a false positive to me", "", false},
		{"mentions the words but not as a prefix", "I don't think this is a false positive: it's real", "", false},
		{"empty text", "", "", false},
		{"whitespace only", "   ", "", false},
		{"prefix without colon does not match", "false positive this is fine", "", false},
		{"partial prefix does not match", "false posit: nope", "", false},
		// Prefix consumption is rune-based (mirroring plan.MatchRevise's
		// own hardening against a real Unicode byte-offset bug, verdict.go)
		// -- a multi-byte reason immediately after the ASCII prefix must
		// come through byte-for-byte untouched, never corrupted by a cut
		// point that assumed every consumed prefix rune was exactly one
		// byte.
		{"multibyte reason preserved byte-for-byte", "false positive: café is fine 日本語", "café is fine 日本語", true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reason, ok := falsepositive.Match(tc.text)
			if ok != tc.wantOK {
				t.Fatalf("Match(%q) ok = %v, want %v", tc.text, ok, tc.wantOK)
			}
			if reason != tc.wantReason {
				t.Errorf("Match(%q) reason = %q, want %q", tc.text, reason, tc.wantReason)
			}
		})
	}
}

func TestValidateReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		reason  string
		wantErr error
	}{
		{"non-empty reason", "intentional dead code", nil},
		{"reason with surrounding whitespace still valid", "  intentional  ", nil},
		{"empty reason", "", falsepositive.ErrEmptyReason},
		{"whitespace-only reason", "   ", falsepositive.ErrEmptyReason},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := falsepositive.ValidateReason(tc.reason)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("ValidateReason(%q) = %v, want nil", tc.reason, err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ValidateReason(%q) = %v, want %v", tc.reason, err, tc.wantErr)
			}
		})
	}
}
