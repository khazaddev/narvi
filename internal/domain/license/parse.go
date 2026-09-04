// This file (parse.go) implements the wire format itself: [Parse] and
// [Verify]. Format, verbatim from docs/design/boundaries-design.md,
// section 1.5:
//
//	narvi1.<base64url(payload JSON)>.<base64url(signature)>
//
// The signature is computed over the literal bytes "narvi1." concatenated
// with the base64url-encoded payload -- i.e. everything up to and
// including the second dot is never included, only the version prefix
// plus the payload segment. The version lives in that prefix, never a
// separate header field a forged token could name to select a different
// verification rule -- mirrors internal/adapters/outbound/oidcsigning's
// own "hardcode the algorithm, never trust the token to name it" idiom
// (design note section 1.5). base64url throughout is unpadded
// (encoding/base64.RawURLEncoding) -- this format has no interop
// requirement with any external system, so this is a free choice, made
// once, here.

package license

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// formatVersion is the wire format's own version literal -- the string
// before the first dot. A payload signed under a hypothetical future
// "narvi2" format can never verify under these rules: the signed message
// includes this literal, so a version bump is automatically
// domain-separated from this one without any extra field.
const formatVersion = "narvi1"

// formatPrefix is formatVersion plus the dot that follows it -- exactly
// the bytes prepended to the payload segment before signing/verifying.
const formatPrefix = formatVersion + "."

// wireClaims is the payload segment's exact JSON shape (the design
// note's own claim list, all required). Unexported: callers see only
// the decoded, verified [Grant] -- this struct exists purely to give
// json.Unmarshal something to decode into.
type wireClaims struct {
	KeyID        string   `json:"kid"`
	Subject      string   `json:"sub"`
	Product      string   `json:"product"`
	IssuedAt     int64    `json:"iat"`
	NotBefore    int64    `json:"nbf"`
	ExpiresAt    int64    `json:"exp"`
	Capabilities []string `json:"caps"`
}

// Parse checks syntax, key id, signature and product, and that every
// named capability is one this build defines -- in that order, because
// nothing about a payload's own CONTENT (product, capabilities) is
// trusted until the signature covering it has verified.
//
// Parse deliberately does NOT check time: it neither consults [Grant.
// ValidAt] nor takes a `now` parameter. A grant's own window is
// re-evaluated on every call by internal/app/capability.Registry, so a
// key that expires mid-process stops enabling anything at the very next
// call, never merely at the next restart -- see [Verify], the one caller
// that pairs this with a time check, for boot-time logging only.
func Parse(raw string, keys Keyset) (Grant, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[0] != formatVersion {
		return Grant{}, ErrMalformed
	}
	payloadB64, sigB64 := parts[1], parts[2]

	payloadBytes, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return Grant{}, ErrMalformed
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return Grant{}, ErrMalformed
	}

	var claims wireClaims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return Grant{}, ErrMalformed
	}

	pub, ok := keys[claims.KeyID]
	if !ok {
		return Grant{}, ErrUnknownKey
	}

	if !ed25519.Verify(pub, []byte(formatPrefix+payloadB64), sig) {
		return Grant{}, ErrBadSignature
	}

	// Everything below trusts claims' own content -- the signature above
	// has already verified it came from a key in keys, unmodified.
	if claims.Product != Product {
		return Grant{}, ErrWrongProduct
	}

	capabilities := make([]Capability, 0, len(claims.Capabilities))
	for _, name := range claims.Capabilities {
		c := Capability(name)
		if !isKnownCapability(c) {
			return Grant{}, ErrUnknownCapability
		}
		capabilities = append(capabilities, c)
	}

	return Grant{
		KeyID:        claims.KeyID,
		Subject:      claims.Subject,
		Capabilities: capabilities,
		IssuedAt:     time.Unix(claims.IssuedAt, 0).UTC(),
		NotBefore:    time.Unix(claims.NotBefore, 0).UTC(),
		ExpiresAt:    time.Unix(claims.ExpiresAt, 0).UTC(),
	}, nil
}

// isKnownCapability reports whether c is a member of All -- the same
// closed-vocabulary check [Parse] applies to every capability a grant
// names.
func isKnownCapability(c Capability) bool {
	for _, known := range All {
		if known == c {
			return true
		}
	}
	return false
}

// Verify is Parse plus a single [Grant.ValidAt] check, FOR BOOT-TIME
// LOGGING ONLY -- never called by internal/app/capability.Registry itself
// (which holds the *Grant [Parse] returned and re-derives validity on
// every [internal/app/capability.Registry.Enabled] call instead, so it
// can keep distinguishing "not yet valid" from "expired" from "not
// licensed at all" for as long as the process runs). Composition-root
// boot logging calls this once, at startup, purely to choose which of
// the design note's own boot-log lines to print (docs/design/
// boundaries-design.md, section 1.3).
func Verify(raw string, keys Keyset, now time.Time, nbfSkew time.Duration) (Grant, error) {
	grant, err := Parse(raw, keys)
	if err != nil {
		return Grant{}, err
	}
	if now.Before(grant.NotBefore.Add(-nbfSkew)) {
		return Grant{}, ErrNotYetValid
	}
	if !now.Before(grant.ExpiresAt) {
		return Grant{}, ErrExpired
	}
	return grant, nil
}
