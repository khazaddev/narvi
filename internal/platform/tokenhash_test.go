package platform

import (
	"encoding/base64"
	"testing"
)

// TestHashToken_DeterministicHexDigest proves HashToken is a pure,
// deterministic function producing a 64-char (SHA-256) hex string, and
// that distinct inputs produce distinct digests -- mirrors
// internal/adapters/inbound/wshub's own identical
// TestHashSandboxToken_DeterministicHexDigest precedent for its sibling
// (but separate, see this file's own doc comment) hashing mechanism.
func TestHashToken_DeterministicHexDigest(t *testing.T) {
	t.Parallel()

	tests := []string{"", "a", "ws-token-abc123", "with spaces and 🎉 unicode"}

	for _, tok := range tests {
		t.Run(tok, func(t *testing.T) {
			t.Parallel()

			got1 := HashToken(tok)
			got2 := HashToken(tok)
			if got1 != got2 {
				t.Fatalf("HashToken(%q) not deterministic: %q != %q", tok, got1, got2)
			}
			if len(got1) != 64 {
				t.Errorf("HashToken(%q) length = %d, want 64 (hex-encoded SHA-256)", tok, len(got1))
			}
			for _, r := range got1 {
				isHexDigit := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
				if !isHexDigit {
					t.Fatalf("HashToken(%q) = %q contains non-hex character %q", tok, got1, r)
				}
			}
		})
	}

	if HashToken("token-a") == HashToken("token-b") {
		t.Error("HashToken produced the same digest for two different inputs")
	}
}

// TestGenerateToken proves GenerateToken produces distinct values across
// calls (high-entropy, not a fixed/predictable string) and that each
// result decodes as valid base64.RawURLEncoding, matching this file's own
// doc comment on GenerateToken's exact encoding.
func TestGenerateToken(t *testing.T) {
	t.Parallel()

	const iterations = 20
	seen := make(map[string]bool, iterations)

	for i := 0; i < iterations; i++ {
		token, err := GenerateToken()
		if err != nil {
			t.Fatalf("GenerateToken() error = %v", err)
		}
		if token == "" {
			t.Fatal("GenerateToken() returned an empty string")
		}
		if seen[token] {
			t.Fatalf("GenerateToken() produced a duplicate value across %d calls: %q", iterations, token)
		}
		seen[token] = true

		decoded, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			t.Fatalf("GenerateToken() = %q does not decode as base64.RawURLEncoding: %v", token, err)
		}
		if len(decoded) != wsTokenByteLength {
			t.Errorf("decoded GenerateToken() length = %d, want %d", len(decoded), wsTokenByteLength)
		}
	}
}
