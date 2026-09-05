// This file (grant.go) defines Grant -- [Parse]'s own successful result
// -- and its two read-only queries, [Grant.Has] and [Grant.ValidAt].

package license

import "time"

// Grant is a successfully decoded and signature-verified licence payload.
// [Parse] never returns a Grant whose signature did not verify, whose
// "kid" was unknown, whose product did not match, or that named a
// capability outside [All] -- by the time a caller holds one, only its
// TIME window is still an open question, which is exactly what
// [Grant.ValidAt] answers and [Parse] deliberately does not (see its own
// doc comment).
type Grant struct {
	// KeyID is the issuer key id ("kid") the signature verified against.
	KeyID string

	// Subject is the opaque customer id ("sub"). Never logged or
	// exposed on any wire response beyond what a future read model
	// deliberately chooses to surface (the design note's own "nothing
	// about the customer's data").
	Subject string

	// Capabilities is the granted set ("caps"), already validated
	// against [All] by [Parse] -- every element is a Capability this
	// build defines.
	Capabilities []Capability

	// IssuedAt is the "iat" claim.
	IssuedAt time.Time

	// NotBefore is the "nbf" claim -- the start of the grant's validity
	// window, widened by nbfSkew in [Grant.ValidAt].
	NotBefore time.Time

	// ExpiresAt is the "exp" claim -- the end of the grant's validity
	// window. Never widened, by any skew, in either direction (the design
	// note's own "clock skew widens nbf only, never exp").
	ExpiresAt time.Time
}

// Has reports whether c is in g.Capabilities.
func (g Grant) Has(c Capability) bool {
	for _, granted := range g.Capabilities {
		if granted == c {
			return true
		}
	}
	return false
}

// ValidAt is the ONLY time check this package performs, and the ONLY one
// internal/app/capability.Registry.Enabled ever calls -- re-evaluated on
// every call, never cached, so a grant that expires mid-process stops
// validating at the very next call (technical plan §34.5).
//
// nbfSkew tolerates a host clock that runs behind the issuer's: now is
// valid against NotBefore as soon as it reaches NotBefore-nbfSkew, and
// NotBefore itself is inclusive (now == NotBefore validates). It never
// widens ExpiresAt in the other direction -- a host clock that is AHEAD
// makes a key expire early, which is the safe direction, and there is no
// grace window after exp: now == ExpiresAt, and anything after it, is
// already expired (exp is an exclusive upper bound, matching RFC 7519's
// own "current time MUST be before exp" convention).
func (g Grant) ValidAt(now time.Time, nbfSkew time.Duration) bool {
	if now.Before(g.NotBefore.Add(-nbfSkew)) {
		return false
	}
	return now.Before(g.ExpiresAt)
}
