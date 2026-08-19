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
		{"opencode exact prefix boundary", "OPENCODE_", ErrNameReservedOpenCodeNamespace},
		{"opencode reserved custom config slot", "OPENCODE_CONFIG", ErrNameReservedOpenCodeNamespace},
		{"opencode reserved inline config slot", "OPENCODE_CONFIG_CONTENT", ErrNameReservedOpenCodeNamespace},
		{"opencode reserved future var still rejected by prefix", "OPENCODE_SOME_FUTURE_VAR", ErrNameReservedOpenCodeNamespace},
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

// TestValidateName_RejectsOpenCodeConfigContent is a direct regression test
// for the adversarial-review CRITICAL finding: before this fix, nothing in
// ValidateName rejected a sandbox_secrets row named "OPENCODE_CONFIG_CONTENT"
// -- OpenCode's own documented "inline config" env var, which sits ABOVE
// even the project slot (the one Step 48's sentinel-fix capability
// restriction targets) in OpenCode's real, verified precedence order (see
// cmd/sandbox-agent/opencodeconfig.go's own top doc comment). A maintainer
// holding ActionManageEnvSecrets could therefore have saved exactly this
// name with a value re-enabling unrestricted tools, and
// applySandboxSecretEnv/opencodeproc.Spawn would have threaded it straight
// into `opencode serve`'s own env, silently overriding the
// capability-restricted sentinel-fix child session's own restriction --
// defeating §27.2's own stated structural guarantee ("a customer-authored
// config can never override the security-relevant agent restriction") by
// the engine's own documented ordering. This test proves that specific
// name is rejected at validation -- i.e. it can never be saved as a
// sandbox_secrets row in the first place, so it can never reach the
// delivery/injection path at all. See
// TestCapabilityRestrictedProjectConfig_NeverTouchedBySandboxSecretInjection
// (cmd/sandbox-agent) for the complementary, separate proof that this
// binary's own injection mechanism has no OTHER way to reach the project
// config file even if a name like this somehow arrived in a resolved map.
func TestValidateName_RejectsOpenCodeConfigContent(t *testing.T) {
	err := ValidateName("OPENCODE_CONFIG_CONTENT")
	if !errors.Is(err, ErrNameReservedOpenCodeNamespace) {
		t.Errorf("ValidateName(%q) = %v, want ErrNameReservedOpenCodeNamespace", "OPENCODE_CONFIG_CONTENT", err)
	}
}

// TestValidateName_OpenCodePrefixTakesPriorityOverShape mirrors
// TestValidateName_NarviPrefixTakesPriorityOverShape exactly, for the new
// OPENCODE_ reservation -- pins the observable error identity a caller
// might branch on.
func TestValidateName_OpenCodePrefixTakesPriorityOverShape(t *testing.T) {
	err := ValidateName("OPENCODE_CUSTOM_THING")
	if !errors.Is(err, ErrNameReservedOpenCodeNamespace) {
		t.Errorf("ValidateName(%q) = %v, want ErrNameReservedOpenCodeNamespace", "OPENCODE_CUSTOM_THING", err)
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
