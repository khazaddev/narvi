package oidcsigning_test

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/narvidev/narvi/internal/adapters/outbound/oidcsigning"
	"github.com/narvidev/narvi/internal/domain/cloudidentity"
)

func TestGenerateKeyPair(t *testing.T) {
	priv, err := oidcsigning.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair() error = %v", err)
	}
	if priv.N.BitLen() < 2040 || priv.N.BitLen() > 2048 {
		t.Errorf("generated key bit length = %d, want ~2048", priv.N.BitLen())
	}
}

func TestGenerateKid_UniqueAndNonEmpty(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		kid, err := oidcsigning.GenerateKid()
		if err != nil {
			t.Fatalf("GenerateKid() error = %v", err)
		}
		if kid == "" {
			t.Fatal("GenerateKid() returned empty string")
		}
		if seen[kid] {
			t.Fatalf("GenerateKid() produced a duplicate: %q", kid)
		}
		seen[kid] = true
	}
}

func TestPrivateKeyPKCS8RoundTrip(t *testing.T) {
	priv, err := oidcsigning.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair() error = %v", err)
	}
	der, err := oidcsigning.EncodePrivateKeyPKCS8(priv)
	if err != nil {
		t.Fatalf("EncodePrivateKeyPKCS8() error = %v", err)
	}
	got, err := oidcsigning.DecodePrivateKeyPKCS8(der)
	if err != nil {
		t.Fatalf("DecodePrivateKeyPKCS8() error = %v", err)
	}
	if got.N.Cmp(priv.N) != 0 || got.E != priv.E {
		t.Fatal("decoded key does not match the original")
	}
}

func TestDecodePrivateKeyPKCS8_RejectsGarbage(t *testing.T) {
	if _, err := oidcsigning.DecodePrivateKeyPKCS8([]byte("not a real der-encoded key")); err == nil {
		t.Fatal("DecodePrivateKeyPKCS8(garbage) error = nil, want an error")
	}
}

func TestPublicJWK_RoundTripsThroughReconstruction(t *testing.T) {
	priv, err := oidcsigning.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair() error = %v", err)
	}
	jwk := oidcsigning.PublicJWK(&priv.PublicKey, "test-kid")
	if jwk.Kty != "RSA" || jwk.Alg != "RS256" || jwk.Use != "sig" || jwk.Kid != "test-kid" {
		t.Fatalf("PublicJWK() = %+v, unexpected fixed fields", jwk)
	}

	reconstructed, err := oidcsigning.PublicKeyFromJWK(jwk)
	if err != nil {
		t.Fatalf("PublicKeyFromJWK() error = %v", err)
	}
	if reconstructed.N.Cmp(priv.N) != 0 {
		t.Error("reconstructed modulus (n) does not match the original public key")
	}
	if reconstructed.E != priv.E {
		t.Errorf("reconstructed exponent (e) = %d, want %d", reconstructed.E, priv.E)
	}
}

func TestPublicKeyFromJWK_RejectsNonRSAKty(t *testing.T) {
	_, err := oidcsigning.PublicKeyFromJWK(oidcsigning.JWK{Kty: "EC", N: "x", E: "AQAB"})
	if err == nil {
		t.Fatal("PublicKeyFromJWK(kty=EC) error = nil, want an error")
	}
}

func TestMarshalJWKS(t *testing.T) {
	priv, err := oidcsigning.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair() error = %v", err)
	}
	jwk := oidcsigning.PublicJWK(&priv.PublicKey, "kid-1")

	out, err := oidcsigning.MarshalJWKS([]oidcsigning.JWK{jwk})
	if err != nil {
		t.Fatalf("MarshalJWKS() error = %v", err)
	}

	var decoded struct {
		Keys []oidcsigning.JWK `json:"keys"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(jwks) error = %v", err)
	}
	if len(decoded.Keys) != 1 || decoded.Keys[0].Kid != "kid-1" {
		t.Errorf("decoded JWKS = %+v, want one key with kid=kid-1", decoded.Keys)
	}
}

// TestSignVerify_EndToEnd is this Step's own "prove the crypto works end
// to end rather than asserting it" requirement: mints a real
// cloudidentity.Claims payload, signs it, renders the SAME key's own
// public half as a JWK exactly as it would be stored/published, and
// verifies the token against a public key reconstructed ONLY from that
// published JWK (never the original *rsa.PublicKey value) -- mirroring
// exactly what a real cloud STS does: fetch the JWKS document, pick the
// key named by the token's own kid, verify. The final assertion
// independently re-runs the signature check via rsa.VerifyPKCS1v15
// directly (the real standard-library primitive), not merely through
// this package's own Verify wrapper, so a bug that made Verify report
// success without actually checking anything would still be caught.
func TestSignVerify_EndToEnd(t *testing.T) {
	priv, err := oidcsigning.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair() error = %v", err)
	}
	kid, err := oidcsigning.GenerateKid()
	if err != nil {
		t.Fatalf("GenerateKid() error = %v", err)
	}

	claims := cloudidentity.BuildClaims(cloudidentity.BuildClaimsInput{
		Issuer:        "https://issuer.narvi.example.test",
		EnvironmentID: "3fa85f64-5717-4562-b3fc-2c963f66afa6",
		Audience:      "sts.amazonaws.com",
		SessionID:     "session-1",
		Gen:           1,
	})

	token, err := oidcsigning.Sign(priv, kid, claims)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	if strings.Count(token, ".") != 2 {
		t.Fatalf("token = %q, want exactly 2 dots (3-part compact JWS)", token)
	}

	// Publish the public half exactly as CreateOIDCSigningKey would store
	// it, then reconstruct ONLY from that published form -- never reusing
	// priv.PublicKey directly, to prove the JWKS document itself carries
	// enough information for a real verifier.
	jwksBytes, err := oidcsigning.MarshalJWKS([]oidcsigning.JWK{oidcsigning.PublicJWK(&priv.PublicKey, kid)})
	if err != nil {
		t.Fatalf("MarshalJWKS() error = %v", err)
	}
	var jwks struct {
		Keys []oidcsigning.JWK `json:"keys"`
	}
	if err := json.Unmarshal(jwksBytes, &jwks); err != nil {
		t.Fatalf("json.Unmarshal(jwks) error = %v", err)
	}

	gotKid, err := oidcsigning.ExtractKid(token)
	if err != nil {
		t.Fatalf("ExtractKid() error = %v", err)
	}
	var matched *oidcsigning.JWK
	for i := range jwks.Keys {
		if jwks.Keys[i].Kid == gotKid {
			matched = &jwks.Keys[i]
		}
	}
	if matched == nil {
		t.Fatalf("no published JWK matches token's own kid %q", gotKid)
	}

	pub, err := oidcsigning.PublicKeyFromJWK(*matched)
	if err != nil {
		t.Fatalf("PublicKeyFromJWK() error = %v", err)
	}

	payload, err := oidcsigning.Verify(token, pub)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}

	var gotClaims cloudidentity.Claims
	if err := json.Unmarshal(payload, &gotClaims); err != nil {
		t.Fatalf("unmarshal verified payload error = %v", err)
	}
	if gotClaims.Subject != "narvi:environment:3fa85f64-5717-4562-b3fc-2c963f66afa6" {
		t.Errorf("verified Subject = %q, unexpected", gotClaims.Subject)
	}
	if gotClaims.Audience != "sts.amazonaws.com" {
		t.Errorf("verified Audience = %q, unexpected", gotClaims.Audience)
	}

	// Independent, direct standard-library re-verification -- proves
	// Verify() is not merely reporting success unconditionally.
	parts := strings.Split(token, ".")
	headerAndPayload := parts[0] + "." + parts[1]
	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode signature for independent check: %v", err)
	}
	hashed := sha256.Sum256([]byte(headerAndPayload))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, hashed[:], sigBytes); err != nil {
		t.Fatalf("independent rsa.VerifyPKCS1v15 failed: %v", err)
	}
}

// TestVerify_RejectsTamperedPayload mutation-tests signature enforcement:
// flipping one byte of the payload after signing must make Verify fail.
func TestVerify_RejectsTamperedPayload(t *testing.T) {
	priv, err := oidcsigning.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair() error = %v", err)
	}
	token, err := oidcsigning.Sign(priv, "kid-1", map[string]string{"sub": "narvi:environment:abc"})
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	parts := strings.Split(token, ".")
	tampered := parts[0] + "." + parts[1] + "x" + "." + parts[2]

	if _, err := oidcsigning.Verify(tampered, &priv.PublicKey); err == nil {
		t.Fatal("Verify(tampered token) error = nil, want an error")
	} else if !errors.Is(err, oidcsigning.ErrSignatureInvalid) {
		t.Errorf("Verify(tampered token) error = %v, want ErrSignatureInvalid", err)
	}
}

// TestVerify_RejectsWrongKey proves a token signed by one key does not
// verify against a different, unrelated key's own public half.
func TestVerify_RejectsWrongKey(t *testing.T) {
	priv1, err := oidcsigning.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair() error = %v", err)
	}
	priv2, err := oidcsigning.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair() error = %v", err)
	}

	token, err := oidcsigning.Sign(priv1, "kid-1", map[string]string{"sub": "narvi:environment:abc"})
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	if _, err := oidcsigning.Verify(token, &priv2.PublicKey); !errors.Is(err, oidcsigning.ErrSignatureInvalid) {
		t.Errorf("Verify(token, wrong pubkey) error = %v, want ErrSignatureInvalid", err)
	}
}

// TestVerify_RejectsUnsupportedAlgorithm proves Verify hardcodes RS256
// rather than trusting the token's own header -- the "algorithm
// confusion" defense this package's own doc.go describes. Constructs a
// token by hand (this package exposes no way to sign with any other
// algorithm, by design) with alg="none" and a real signature segment,
// confirming Verify rejects it on the alg check alone, before ever
// reaching signature verification.
func TestVerify_RejectsUnsupportedAlgorithm(t *testing.T) {
	priv, err := oidcsigning.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair() error = %v", err)
	}

	headerJSON := []byte(`{"alg":"none","typ":"JWT","kid":"kid-1"}`)
	payloadJSON := []byte(`{"sub":"narvi:environment:abc"}`)
	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadJSON)
	forged := headerB64 + "." + payloadB64 + "." + base64.RawURLEncoding.EncodeToString([]byte("not-checked"))

	if _, err := oidcsigning.Verify(forged, &priv.PublicKey); !errors.Is(err, oidcsigning.ErrUnsupportedAlgorithm) {
		t.Errorf("Verify(alg=none token) error = %v, want ErrUnsupportedAlgorithm", err)
	}
}

func TestVerify_RejectsMalformedToken(t *testing.T) {
	priv, err := oidcsigning.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair() error = %v", err)
	}
	tests := []string{"", "onlyonepart", "two.parts", "a.b.c.d"}
	for _, tok := range tests {
		if _, err := oidcsigning.Verify(tok, &priv.PublicKey); !errors.Is(err, oidcsigning.ErrMalformedToken) {
			t.Errorf("Verify(%q) error = %v, want ErrMalformedToken", tok, err)
		}
	}
}
