package githubapp

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"testing"
	"time"

	"github.com/khazaddev/narvi/internal/adapters/outbound/oidcsigning"
)

// TestSignAppJWT_ClaimsAndSignature proves signAppJWT produces a token
// that (a) verifies against the signing key's own public half and (b)
// carries the exact claim shape GitHub's App-authentication JWT requires:
// "iat" backdated by appJWTClockSkewBudget, "exp" exactly jwtTTL past
// "iat" (so it is bounded by whatever GitHubAppJWTTTL the caller
// configured, itself kept under GitHub's own hard 10-minute ceiling), and
// "iss" equal to the App's own id.
func TestSignAppJWT_ClaimsAndSignature(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate test key: %v", err)
	}

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	const appID = int64(555)
	const jwtTTL = 3 * time.Minute

	token, err := signAppJWT(appID, key, jwtTTL, now)
	if err != nil {
		t.Fatalf("signAppJWT() error = %v, want nil", err)
	}

	payload, err := oidcsigning.Verify(token, &key.PublicKey)
	if err != nil {
		t.Fatalf("oidcsigning.Verify(signAppJWT() token) error = %v, want a verifying signature", err)
	}

	var claims appJWTClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}

	wantIat := now.Add(-appJWTClockSkewBudget).Unix()
	if claims.IssuedAt != wantIat {
		t.Errorf("claims.IssuedAt = %d, want %d (now - %s clock-skew budget)", claims.IssuedAt, wantIat, appJWTClockSkewBudget)
	}
	wantExp := now.Add(jwtTTL).Unix()
	if claims.ExpiresAt != wantExp {
		t.Errorf("claims.ExpiresAt = %d, want %d (now + jwtTTL)", claims.ExpiresAt, wantExp)
	}
	if claims.Issuer != appID {
		t.Errorf("claims.Issuer = %d, want %d", claims.Issuer, appID)
	}

	// GitHub rejects a JWT whose "exp" is more than 10 minutes past "iat" --
	// this is the hard external ceiling GitHubAppJWTTTL's own doc comment
	// names; prove THIS test's own claims genuinely respect it (a
	// regression that widened jwtTTL past the ceiling should fail here,
	// not silently pass every other assertion above).
	if gap := claims.ExpiresAt - claims.IssuedAt; gap > 600 {
		t.Errorf("exp - iat = %ds, want <= 600s (GitHub's own hard ceiling)", gap)
	}
}

// TestSignAppJWT_DifferentKeyFailsVerification proves a token signed by
// one key does not verify against a different key's public half -- a
// minimal sanity check that signAppJWT is not accidentally producing an
// unsigned or trivially-forgeable token.
func TestSignAppJWT_DifferentKeyFailsVerification(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate test key: %v", err)
	}
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate other test key: %v", err)
	}

	token, err := signAppJWT(1, key, time.Minute, time.Now())
	if err != nil {
		t.Fatalf("signAppJWT() error = %v, want nil", err)
	}

	if _, err := oidcsigning.Verify(token, &otherKey.PublicKey); err == nil {
		t.Error("oidcsigning.Verify(token, otherKey) = nil error, want a verification failure against the wrong key")
	}
}
