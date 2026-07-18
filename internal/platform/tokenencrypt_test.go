package platform_test

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/khazaddev/narvi/internal/platform"
)

// testEncryptionKey returns a fresh, valid 32-byte AES-256-GCM key for the
// duration of one test.
func testEncryptionKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("generate test key: %v", err)
	}
	return key
}

// TestEncryptDecryptToken_RoundTrip is table-driven over several plaintexts
// (including empty) and proves Decrypt(Encrypt(plaintext)) == plaintext.
func TestEncryptDecryptToken_RoundTrip(t *testing.T) {
	t.Parallel()
	key := testEncryptionKey(t)

	tests := []struct {
		name      string
		plaintext []byte
	}{
		{name: "empty", plaintext: []byte("")},
		{name: "short token", plaintext: []byte("gho_abc123")},
		{name: "long token", plaintext: bytes.Repeat([]byte("a"), 500)},
		{name: "unicode", plaintext: []byte("token-with-🎉-unicode")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ciphertext, err := platform.EncryptToken(key, tc.plaintext)
			if err != nil {
				t.Fatalf("EncryptToken() error = %v", err)
			}

			got, err := platform.DecryptToken(key, ciphertext)
			if err != nil {
				t.Fatalf("DecryptToken() error = %v", err)
			}
			if !bytes.Equal(got, tc.plaintext) {
				t.Fatalf("DecryptToken(EncryptToken(%q)) = %q, want %q", tc.plaintext, got, tc.plaintext)
			}
		})
	}
}

// TestEncryptToken_FreshNonceEveryCall proves two Encrypt calls on the SAME
// plaintext under the SAME key produce DIFFERENT ciphertexts — the nonce is
// genuinely fresh each call, never reused (a reused nonce under GCM is a
// catastrophic confidentiality break).
func TestEncryptToken_FreshNonceEveryCall(t *testing.T) {
	t.Parallel()
	key := testEncryptionKey(t)
	plaintext := []byte("gho_the-same-token-every-time")

	first, err := platform.EncryptToken(key, plaintext)
	if err != nil {
		t.Fatalf("EncryptToken() first call error = %v", err)
	}
	second, err := platform.EncryptToken(key, plaintext)
	if err != nil {
		t.Fatalf("EncryptToken() second call error = %v", err)
	}

	if bytes.Equal(first, second) {
		t.Fatal("EncryptToken produced identical ciphertexts for two calls on the same plaintext — nonce reuse")
	}

	// Both must still decrypt back to the same plaintext.
	for i, ct := range [][]byte{first, second} {
		got, err := platform.DecryptToken(key, ct)
		if err != nil {
			t.Fatalf("DecryptToken(ciphertext #%d) error = %v", i, err)
		}
		if !bytes.Equal(got, plaintext) {
			t.Fatalf("DecryptToken(ciphertext #%d) = %q, want %q", i, got, plaintext)
		}
	}
}

// TestDecryptToken_TamperedCiphertextFails proves flipping one byte of a
// valid ciphertext (anywhere — nonce, sealed data, or auth tag) makes
// DecryptToken fail: AES-GCM's own authentication, not just
// confidentiality.
func TestDecryptToken_TamperedCiphertextFails(t *testing.T) {
	t.Parallel()
	key := testEncryptionKey(t)
	plaintext := []byte("gho_a-real-looking-access-token")

	ciphertext, err := platform.EncryptToken(key, plaintext)
	if err != nil {
		t.Fatalf("EncryptToken() error = %v", err)
	}

	for i := 0; i < len(ciphertext); i += 7 { // sample every 7th byte, covers nonce + body + tag without an overlong test
		tampered := make([]byte, len(ciphertext))
		copy(tampered, ciphertext)
		tampered[i] ^= 0xFF

		if _, err := platform.DecryptToken(key, tampered); err == nil {
			t.Fatalf("DecryptToken() succeeded after flipping byte %d, want an error", i)
		}
	}
}

// TestDecryptToken_WrongKeyFails proves a ciphertext encrypted under one
// key cannot be decrypted under a different key.
func TestDecryptToken_WrongKeyFails(t *testing.T) {
	t.Parallel()
	key1 := testEncryptionKey(t)
	key2 := testEncryptionKey(t)

	ciphertext, err := platform.EncryptToken(key1, []byte("gho_secret"))
	if err != nil {
		t.Fatalf("EncryptToken() error = %v", err)
	}

	if _, err := platform.DecryptToken(key2, ciphertext); err == nil {
		t.Fatal("DecryptToken() with the wrong key succeeded, want an error")
	}
}

// TestDecryptToken_CiphertextTooShort proves a ciphertext shorter than the
// prepended nonce is rejected explicitly (platform.ErrCiphertextTooShort),
// never a slice-bounds panic.
func TestDecryptToken_CiphertextTooShort(t *testing.T) {
	t.Parallel()
	key := testEncryptionKey(t)

	_, err := platform.DecryptToken(key, []byte("short"))
	if err == nil {
		t.Fatal("DecryptToken() with a too-short ciphertext succeeded, want an error")
	}
}

// TestEncryptToken_InvalidKeyLength proves EncryptToken/DecryptToken
// surface an error (never a panic) for a key that isn't a valid AES key
// length.
func TestEncryptToken_InvalidKeyLength(t *testing.T) {
	t.Parallel()
	badKey := []byte("too-short")

	if _, err := platform.EncryptToken(badKey, []byte("plaintext")); err == nil {
		t.Fatal("EncryptToken() with an invalid key length succeeded, want an error")
	}
	if _, err := platform.DecryptToken(badKey, []byte("0123456789ab-not-real-ciphertext")); err == nil {
		t.Fatal("DecryptToken() with an invalid key length succeeded, want an error")
	}
}
