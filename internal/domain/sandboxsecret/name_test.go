package sandboxsecret

import (
	"errors"
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/providercredential"
)

func TestValidateName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr error // nil means "no error"
	}{
		{"ordinary uppercase name", "MY_SECRET", nil},
		{"single letter", "A", nil},
		{"leading underscore", "_PRIVATE_KEY", nil},
		{"digits after first char", "SECRET_2", nil},
		{"empty", "", ErrNameEmpty},
		{"lowercase rejected", "my_secret", ErrNameShape},
		{"mixed case rejected", "My_Secret", ErrNameShape},
		{"starts with digit", "2FA_TOKEN", ErrNameShape},
		{"contains space", "MY SECRET", ErrNameShape},
		{"contains hyphen", "MY-SECRET", ErrNameShape},
		{"contains dot", "MY.SECRET", ErrNameShape},
		{"contains equals", "MY=SECRET", ErrNameShape},
		{"contains NUL", "MY_SECRET\x00", ErrNameShape},
		{"narvi exact prefix boundary", "NARVI_", ErrNameReservedNarviNamespace},
		{"narvi reserved boot mode", "NARVI_BOOT_MODE", ErrNameReservedNarviNamespace},
		{"narvi reserved session config", "NARVI_SESSION_CONFIG", ErrNameReservedNarviNamespace},
		{"narvi reserved future var still rejected by prefix", "NARVI_SOME_FUTURE_VAR", ErrNameReservedNarviNamespace},
		{"provider reserved anthropic", "ANTHROPIC_API_KEY", ErrNameReservedProviderCredential},
		{"provider reserved openai", "OPENAI_API_KEY", ErrNameReservedProviderCredential},
		{"provider reserved google api key", "GOOGLE_API_KEY", ErrNameReservedProviderCredential},
		{"provider reserved google generative", "GOOGLE_GENERATIVE_AI_API_KEY", ErrNameReservedProviderCredential},
		{"provider reserved gemini", "GEMINI_API_KEY", ErrNameReservedProviderCredential},
		{"too long", strings.Repeat("A", maxNameLength+1), ErrNameTooLong},
		{"exactly at max length is fine", strings.Repeat("A", maxNameLength), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateName(tc.input)
			if tc.wantErr == nil {
				if err != nil {
					t.Errorf("ValidateName(%q) = %v, want nil", tc.input, err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("ValidateName(%q) = %v, want error wrapping %v", tc.input, err, tc.wantErr)
			}
		})
	}
}

// TestValidateName_NarviPrefixTakesPriorityOverShape confirms a name that
// is BOTH NARVI_*-prefixed and otherwise POSIX-shaped fails specifically
// as ErrNameReservedNarviNamespace, not ErrNameShape -- the two checks
// never actually conflict in practice (a NARVI_ prefix is itself POSIX
// shaped), but this pins the observable error identity a caller might
// branch on.
func TestValidateName_NarviPrefixTakesPriorityOverShape(t *testing.T) {
	err := ValidateName("NARVI_CUSTOM_THING")
	if !errors.Is(err, ErrNameReservedNarviNamespace) {
		t.Errorf("ValidateName(%q) = %v, want ErrNameReservedNarviNamespace", "NARVI_CUSTOM_THING", err)
	}
}

// TestValidateName_EveryProviderCredentialEnvVarNameIsRejected is an
// exhaustive, non-hardcoded check that ValidateName rejects EVERY name
// providercredential.AllEnvVarNames currently returns, ranged over rather
// than copy-pasted -- so if a future Step adds a 4th provider (or a new
// env-var alias for an existing one), this test starts failing the moment
// ValidateName's own rejection set silently falls out of sync, rather
// than only when someone remembers to hand-add a new row above.
func TestValidateName_EveryProviderCredentialEnvVarNameIsRejected(t *testing.T) {
	for _, reserved := range providercredential.AllEnvVarNames() {
		t.Run(reserved, func(t *testing.T) {
			err := ValidateName(reserved)
			if !errors.Is(err, ErrNameReservedProviderCredential) {
				t.Errorf("ValidateName(%q) = %v, want ErrNameReservedProviderCredential", reserved, err)
			}
		})
	}
}
