package oidcsigning

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
)

// JWK is an RSA public key rendered as a JSON Web Key (RFC 7517 §6.3,
// "Parameters for RSA Public Keys") -- the exact shape the JWKS endpoint
// publishes each key as (wrapped in {"keys": [...]}, RFC 7517 §5).
// Deliberately NOT wire-contract-generated (contracts/rest/v1) -- see
// internal/domain/cloudidentity.Claims' own doc comment for why: this is
// an external, standard format arbitrary cloud STS implementations parse,
// not Narvi's own web-UI/sandbox-agent wire contract.
type JWK struct {
	Kty string `json:"kty"`
	N   string `json:"n"`
	E   string `json:"e"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`
}

// base64URLEncode encodes b as unpadded base64url (RFC 7515 §2's own
// "base64url" -- no '+'/'/' and no trailing '=' padding), the encoding
// every JWT/JWK field in this package uses.
func base64URLEncode(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// base64URLDecode reverses base64URLEncode.
func base64URLDecode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

// PublicJWK renders pub as a JWK, under kid, marked for RS256 signature
// use -- the value CreateOIDCSigningKey persists into oidc_signing_keys.
// public_jwk, pre-rendered once at generation time (never recomputed per
// JWKS request -- see migrations/000092_oidc_signing_keys.up.sql's own
// doc comment on why).
func PublicJWK(pub *rsa.PublicKey, kid string) JWK {
	return JWK{
		Kty: "RSA",
		N:   base64URLEncode(pub.N.Bytes()),
		E:   base64URLEncode(bigEndianMinimal(pub.E)),
		Kid: kid,
		Alg: "RS256",
		Use: "sig",
	}
}

// bigEndianMinimal encodes a non-negative int as big-endian bytes with no
// leading zero byte (the minimal encoding RFC 7518 §6.3.1 requires for a
// JWK's own "e" member) -- pub.E is always small (65537, the universal
// standard RSA public exponent crypto/rsa.GenerateKey uses) but this
// makes no assumption about its specific value beyond non-negative.
func bigEndianMinimal(n int) []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(n))
	i := 0
	for i < len(buf)-1 && buf[i] == 0 {
		i++
	}
	return buf[i:]
}

// PublicKeyFromJWK reconstructs an *rsa.PublicKey from jwk -- the inverse
// of PublicJWK, used by a verifier (a real cloud STS in production; this
// package's own round-trip tests in this codebase) that only has the
// published JWK document, not the original key. Returns an error for a
// non-RSA kty or malformed n/e.
func PublicKeyFromJWK(jwk JWK) (*rsa.PublicKey, error) {
	if jwk.Kty != "RSA" {
		return nil, fmt.Errorf("oidcsigning: unsupported jwk kty %q, want RSA", jwk.Kty)
	}
	nBytes, err := base64URLDecode(jwk.N)
	if err != nil {
		return nil, fmt.Errorf("oidcsigning: decode jwk n: %w", err)
	}
	eBytes, err := base64URLDecode(jwk.E)
	if err != nil {
		return nil, fmt.Errorf("oidcsigning: decode jwk e: %w", err)
	}
	if len(eBytes) == 0 {
		return nil, fmt.Errorf("oidcsigning: jwk e is empty")
	}
	ePadded := make([]byte, 8)
	copy(ePadded[8-len(eBytes):], eBytes)

	return &rsa.PublicKey{
		N: new(big.Int).SetBytes(nBytes),
		E: int(binary.BigEndian.Uint64(ePadded)),
	}, nil
}

// MarshalJWKS wraps keys as the standard {"keys": [...]} JWKS document
// (RFC 7517 §5) -- the JWKS endpoint's own response body.
func MarshalJWKS(keys []JWK) ([]byte, error) {
	doc := struct {
		Keys []JWK `json:"keys"`
	}{Keys: keys}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("oidcsigning: marshal jwks: %w", err)
	}
	return out, nil
}
