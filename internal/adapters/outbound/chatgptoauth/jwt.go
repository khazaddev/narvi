package chatgptoauth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// decodeIDTokenAccountID extracts the chatgpt_account_id claim from a JWT
// id_token WITHOUT verifying its signature -- see types.go's own
// idTokenClaims doc comment for why that is deliberate here. A JWT is
// three base64url segments joined by ".": header.payload.signature; only
// the payload (claims) segment is decoded.
func decodeIDTokenAccountID(idToken string) (string, error) {
	segments := strings.Split(idToken, ".")
	if len(segments) != 3 {
		return "", fmt.Errorf("chatgptoauth: id_token has %d segments, want 3 (header.payload.signature)", len(segments))
	}

	// JWTs use base64url WITHOUT padding (RFC 7515 §2) -- RawURLEncoding
	// is the exact matching stdlib codec.
	payload, err := base64.RawURLEncoding.DecodeString(segments[1])
	if err != nil {
		return "", fmt.Errorf("chatgptoauth: decode id_token payload: %w", err)
	}

	var claims idTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("chatgptoauth: unmarshal id_token claims: %w", err)
	}
	if claims.ChatGPTAccountID == "" {
		return "", fmt.Errorf("chatgptoauth: id_token payload carries no chatgpt_account_id claim")
	}
	return claims.ChatGPTAccountID, nil
}
