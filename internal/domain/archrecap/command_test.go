package archrecap_test

import (
	"errors"
	"testing"

	"github.com/narvidev/narvi/internal/domain/archrecap"
)

func TestMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		text       string
		wantReason string
		wantOK     bool
	}{
		{"exact prefix with reason", "arch recap wrong: the alternative wasn't actually rejected", "the alternative wasn't actually rejected", true},
		{"case-insensitive prefix", "Arch Recap Wrong: missed the real decision", "missed the real decision", true},
		{"mixed-case prefix", "ArCh ReCaP WrOnG: nope", "nope", true},
		{"leading/trailing whitespace around whole comment", "   arch recap wrong: nope   ", "nope", true},
		{"extra whitespace between prefix and reason", "arch recap wrong:    nope", "nope", true},
		{"empty reason after prefix", "arch recap wrong:", "", true},
		{"empty reason after prefix with trailing space", "arch recap wrong:   ", "", true},
		{"not a command at all", "this arch recap seems wrong to me", "", false},
		{"mentions the words but not as a prefix", "I think the arch recap wrong here is subtle", "", false},
		{"empty text", "", "", false},
		{"whitespace only", "   ", "", false},
		{"prefix without colon does not match", "arch recap wrong this is fine", "", false},
		{"partial prefix does not match", "arch recap: nope", "", false},
		// Prefix consumption is rune-based, mirroring falsepositive.Match's
		// own identical hardening against a real Unicode byte-offset bug.
		{"multibyte reason preserved byte-for-byte", "arch recap wrong: café decision 日本語", "café decision 日本語", true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reason, ok := archrecap.Match(tc.text)
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
		{"non-empty reason", "missed the real decision", nil},
		{"reason with surrounding whitespace still valid", "  nope  ", nil},
		{"empty reason", "", archrecap.ErrEmptyReason},
		{"whitespace-only reason", "   ", archrecap.ErrEmptyReason},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := archrecap.ValidateReason(tc.reason)
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
