package wshub

import "testing"

// TestHashSandboxToken_DeterministicHexDigest proves HashSandboxToken is a
// pure, deterministic function producing a 64-char (SHA-256) hex string,
// and that distinct inputs produce distinct digests.
func TestHashSandboxToken_DeterministicHexDigest(t *testing.T) {
	t.Parallel()

	tests := []string{"", "a", "sandbox-token-abc123", "with spaces and 🎉 unicode"}

	for _, tok := range tests {
		t.Run(tok, func(t *testing.T) {
			t.Parallel()

			got1 := HashSandboxToken(tok)
			got2 := HashSandboxToken(tok)
			if got1 != got2 {
				t.Fatalf("HashSandboxToken(%q) not deterministic: %q != %q", tok, got1, got2)
			}
			if len(got1) != 64 {
				t.Errorf("HashSandboxToken(%q) length = %d, want 64 (hex-encoded SHA-256)", tok, len(got1))
			}
			for _, r := range got1 {
				isHexDigit := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
				if !isHexDigit {
					t.Fatalf("HashSandboxToken(%q) = %q contains non-hex character %q", tok, got1, r)
				}
			}
		})
	}

	if HashSandboxToken("token-a") == HashSandboxToken("token-b") {
		t.Error("HashSandboxToken produced the same digest for two different inputs")
	}
}

// TestVerifySandboxToken is table-driven over every case Step 18's own
// spec names: empty presented always fails regardless of storedHash; nil
// storedHash (the universal case today, nothing mints one yet) accepts any
// non-empty presented; a matching hash succeeds; a non-matching hash fails.
func TestVerifySandboxToken(t *testing.T) {
	t.Parallel()

	realHash := HashSandboxToken("correct-token")

	tests := []struct {
		name       string
		presented  string
		storedHash *string
		want       bool
	}{
		{
			name:       "empty presented always fails, even with nil storedHash",
			presented:  "",
			storedHash: nil,
			want:       false,
		},
		{
			name:       "empty presented always fails, even against a real hash",
			presented:  "",
			storedHash: &realHash,
			want:       false,
		},
		{
			name:       "nil storedHash accepts any non-empty token (nothing mints one yet)",
			presented:  "anything-non-empty",
			storedHash: nil,
			want:       true,
		},
		{
			name:       "matching hash succeeds",
			presented:  "correct-token",
			storedHash: &realHash,
			want:       true,
		},
		{
			name:       "non-matching hash fails",
			presented:  "wrong-token",
			storedHash: &realHash,
			want:       false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := verifySandboxToken(tc.presented, tc.storedHash); got != tc.want {
				t.Errorf("verifySandboxToken(%q, %v) = %v, want %v", tc.presented, tc.storedHash, got, tc.want)
			}
		})
	}
}
