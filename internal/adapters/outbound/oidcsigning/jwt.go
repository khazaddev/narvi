package oidcsigning

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// header is a compact JWS header (RFC 7515 §4) -- this package only ever
// produces/accepts exactly this shape: RS256, type JWT, and a kid
// identifying which published JWK verifies the signature.
type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

// ErrMalformedToken is returned by Verify/ExtractKid for a string that
// isn't a well-formed 3-part compact JWS.
var ErrMalformedToken = errors.New("oidcsigning: malformed token (want 3 dot-separated base64url parts)")

// ErrUnsupportedAlgorithm is returned by Verify when the token's own
// header names an alg other than RS256 -- Verify hardcodes RS256 rather
// than trusting the header to say so (this package's own doc.go: closing
// the "algorithm confusion" class of JWT vulnerability).
var ErrUnsupportedAlgorithm = errors.New("oidcsigning: unsupported alg (only RS256 is accepted)")

// ErrSignatureInvalid is returned by Verify when the signature does not
// verify against the given public key -- wraps whatever crypto/rsa.
// VerifyPKCS1v15 itself returned, never exposing more detail than that.
var ErrSignatureInvalid = errors.New("oidcsigning: signature verification failed")

// splitSigningInput returns the "header.payload" prefix Sign/Verify both
// hash and sign/verify over -- RFC 7515's own "JWS Signing Input".
func signingInput(headerB64, payloadB64 string) string {
	return headerB64 + "." + payloadB64
}

// Sign renders claims (any JSON-marshalable value -- production callers
// pass internal/domain/cloudidentity.Claims) as a compact RS256 JWT,
// signed by priv, with header {"alg":"RS256","typ":"JWT","kid":kid}.
func Sign(priv *rsa.PrivateKey, kid string, claims any) (string, error) {
	headerJSON, err := json.Marshal(header{Alg: "RS256", Typ: "JWT", Kid: kid})
	if err != nil {
		return "", fmt.Errorf("oidcsigning: marshal header: %w", err)
	}
	payloadJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("oidcsigning: marshal claims: %w", err)
	}

	headerB64 := base64URLEncode(headerJSON)
	payloadB64 := base64URLEncode(payloadJSON)
	input := signingInput(headerB64, payloadB64)

	hashed := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, hashed[:])
	if err != nil {
		return "", fmt.Errorf("oidcsigning: sign: %w", err)
	}

	return input + "." + base64URLEncode(sig), nil
}

// splitToken splits token into its 3 raw (still base64url-encoded) parts,
// or returns ErrMalformedToken.
func splitToken(token string) (headerB64, payloadB64, sigB64 string, err error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", ErrMalformedToken
	}
	return parts[0], parts[1], parts[2], nil
}

// ExtractKid decodes token's own header (WITHOUT verifying its
// signature) and returns its kid -- used by a verifier to look up which
// published JWK to check the signature against BEFORE verification
// itself, mirroring how a real cloud STS selects a key from a JWKS
// document by the token's own unverified kid hint (RFC 7515's own
// documented, standard two-step "read kid, then verify" flow -- reading
// an unverified header field to select a key is safe here specifically
// because Verify below independently confirms the signature against
// whichever key the caller then supplies; ExtractKid's own return value
// is never trusted for anything beyond that key selection).
func ExtractKid(token string) (string, error) {
	headerB64, _, _, err := splitToken(token)
	if err != nil {
		return "", err
	}
	headerJSON, err := base64URLDecode(headerB64)
	if err != nil {
		return "", fmt.Errorf("oidcsigning: decode header: %w", err)
	}
	var h header
	if err := json.Unmarshal(headerJSON, &h); err != nil {
		return "", fmt.Errorf("oidcsigning: unmarshal header: %w", err)
	}
	return h.Kid, nil
}

// Verify checks token's signature against pub, hardcoding RS256 as the
// only accepted algorithm regardless of what the token's own header
// claims (see ErrUnsupportedAlgorithm's own doc comment), and returns the
// raw, still-JSON-encoded claims payload on success -- the caller
// (production: the round-trip test proving the minting endpoint's own
// output verifies; a real cloud STS follows the identical RFC 7515
// procedure) unmarshals it into whatever claims shape it expects
// (internal/domain/cloudidentity.Claims in this codebase's own tests).
// Verify performs NO expiry/audience/issuer check of its own -- purely
// signature verification, RFC 7515's own layering (JWS signature
// validity is independent of JWT claims validity, RFC 7519 §4/§7); a
// caller that also needs claims validated calls that separately, exactly
// like the standard library's own crypto primitives never enforce
// application-level policy.
func Verify(token string, pub *rsa.PublicKey) ([]byte, error) {
	headerB64, payloadB64, sigB64, err := splitToken(token)
	if err != nil {
		return nil, err
	}

	headerJSON, err := base64URLDecode(headerB64)
	if err != nil {
		return nil, fmt.Errorf("oidcsigning: decode header: %w", err)
	}
	var h header
	if err := json.Unmarshal(headerJSON, &h); err != nil {
		return nil, fmt.Errorf("oidcsigning: unmarshal header: %w", err)
	}
	if h.Alg != "RS256" {
		return nil, fmt.Errorf("%w: header alg=%q", ErrUnsupportedAlgorithm, h.Alg)
	}

	sig, err := base64URLDecode(sigB64)
	if err != nil {
		return nil, fmt.Errorf("oidcsigning: decode signature: %w", err)
	}

	hashed := sha256.Sum256([]byte(signingInput(headerB64, payloadB64)))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, hashed[:], sig); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSignatureInvalid, err)
	}

	payloadJSON, err := base64URLDecode(payloadB64)
	if err != nil {
		return nil, fmt.Errorf("oidcsigning: decode payload: %w", err)
	}
	return payloadJSON, nil
}
