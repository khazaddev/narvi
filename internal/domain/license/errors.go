// This file (errors.go) is every typed error [Parse] and [Verify] can
// return -- see docs/design/boundaries-design.md, section 1.2, verbatim.

package license

import "errors"

var (
	// ErrMalformed covers every syntactic defect [Parse] can find before
	// it ever reaches the keyset: a raw string that is empty, has the
	// wrong version prefix, splits into the wrong number of dot-separated
	// parts, or whose payload/signature segment is not valid base64url,
	// or whose decoded payload is not valid JSON.
	ErrMalformed = errors.New("license: malformed key")

	// ErrUnknownKey is returned when the payload's own "kid" claim names
	// no key in the [Keyset] [Parse] was given -- the correct outcome for
	// an EMPTY [Keyset] (see [IssuerKeys]'s own doc comment) and for a
	// key rotated out from under a still-running deployment (the design
	// note's own "rotation" section).
	ErrUnknownKey = errors.New("license: unknown key id")

	// ErrBadSignature is returned when the Ed25519 signature does not
	// verify against the public key [ErrUnknownKey] would otherwise have
	// rejected the lookup for -- a corrupted, truncated, or forged key.
	ErrBadSignature = errors.New("license: signature verification failed")

	// ErrWrongProduct is returned when a signature verifies but the
	// payload's own "product" claim is not [Product] -- a key genuinely
	// issued by this issuer, for a different Narvi-family product.
	ErrWrongProduct = errors.New("license: key is not for this product")

	// ErrUnknownCapability is returned when a signature verifies but the
	// payload's own "caps" claim names something outside [All] -- a key
	// issued for a capability this build's own vocabulary does not (or
	// does not yet) define.
	ErrUnknownCapability = errors.New("license: key names a capability this build does not define")

	// ErrNotYetValid is [Verify]'s own error (never [Parse]'s -- see
	// [Parse]'s doc comment) for a grant whose "nbf" claim, widened by the
	// caller's nbfSkew, is still in the future.
	ErrNotYetValid = errors.New("license: key not yet valid (check the host clock)")

	// ErrExpired is [Verify]'s own error for a grant whose "exp" claim has
	// passed, with no grace window (the design note's own "no post-exp
	// grace").
	ErrExpired = errors.New("license: key expired")
)
