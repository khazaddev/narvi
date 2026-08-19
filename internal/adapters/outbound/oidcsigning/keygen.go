package oidcsigning

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"fmt"
)

// rsaKeyBits is the RSA modulus size every generated signing key uses --
// 2048 bits, the standard minimum for RS256 in production use (matches
// every major cloud IdP's own default OIDC signing-key size; NIST's own
// current guidance treats 2048-bit RSA as acceptable through at least
// 2030). A plain package constant, not a platform.Timeouts field -- this
// is a key-size, not a duration/interval, mirroring
// tokenEncryptNonceSize's own identical "byte-count constant, not a
// timeout" precedent (internal/platform/tokenencrypt.go).
const rsaKeyBits = 2048

// kidByteLength is how many random bytes GenerateKid reads before
// hex-encoding -- 16 bytes -> 32 hex characters, comfortably collision-
// resistant for a value this codebase treats a duplicate of as
// "unreachable in practice" (see postgres.OIDCSigningKeyStore.Rotate's
// own doc comment) rather than specially handled.
const kidByteLength = 16

// GenerateKeyPair returns a fresh RSA private key (rsaKeyBits, sourced
// from crypto/rand) -- the ONE place in this codebase that generates a
// cloud-identity signing key's own material. Called exactly once per
// rotation (internal/adapters/outbound/postgres.OIDCSigningKeyStore.
// Rotate's own caller, the admin rotation handler).
func GenerateKeyPair() (*rsa.PrivateKey, error) {
	priv, err := rsa.GenerateKey(rand.Reader, rsaKeyBits)
	if err != nil {
		return nil, fmt.Errorf("oidcsigning: generate rsa key pair: %w", err)
	}
	return priv, nil
}

// GenerateKid returns a fresh, random, URL-safe kid (JWT header "kid" /
// JWK "kid") -- kidByteLength random bytes, hex-encoded. Opaque and
// unpredictable by design (§27.3 names no format for kid; a random value
// carries no information an attacker could use, unlike a sequential or
// timestamp-derived one).
func GenerateKid() (string, error) {
	buf := make([]byte, kidByteLength)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("oidcsigning: generate kid: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// EncodePrivateKeyPKCS8 DER-encodes priv as PKCS8 -- the standard,
// algorithm-agnostic private-key encoding (x509.MarshalPKCS8PrivateKey),
// suitable input to platform.EncryptToken for encryption at rest
// (oidc_signing_keys.private_key_encrypted, §27.3).
func EncodePrivateKeyPKCS8(priv *rsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("oidcsigning: marshal pkcs8 private key: %w", err)
	}
	return der, nil
}

// DecodePrivateKeyPKCS8 reverses EncodePrivateKeyPKCS8 -- parses der
// (already decrypted via platform.DecryptToken) back into an *rsa.
// PrivateKey. Returns an error for anything that isn't a valid PKCS8-
// encoded RSA private key (a corrupted/tampered stored value, or a key of
// a type this package never generates).
func DecodePrivateKeyPKCS8(der []byte) (*rsa.PrivateKey, error) {
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("oidcsigning: parse pkcs8 private key: %w", err)
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("oidcsigning: parsed pkcs8 key is %T, want *rsa.PrivateKey", key)
	}
	return rsaKey, nil
}
