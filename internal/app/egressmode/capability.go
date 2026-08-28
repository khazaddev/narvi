// This file (capability.go) defines Capability -- the egress capability
// token §30.2/§30.6/§30.8 all reference as the shared idiom the live/
// shadow transport constructors and this package's own resolver are
// built around. See doc.go for the package-level "why".

package egressmode

// Capability is an opaque, unforgeable token asserting that LIVE
// (pass-through) egress is permitted for the one call that resolved it.
//
// Its zero value -- Capability{} -- is always the SHADOW capability:
// Live reports false, Suppressed reports true. That is deliberate, not
// incidental. The one field below is unexported, so no package outside
// egressmode can construct a Capability whose Live() is true by any means
// other than receiving one this package itself already produced by
// calling Resolve. A caller that zero-initializes this type by mistake,
// receives one from a helper that forgot to call Resolve, holds a struct
// field that was never assigned, or copies a Capability{} literal because
// it looked like the obvious placeholder -- gets suppression, never live,
// with no way to do otherwise short of literally copying a Capability
// this package's own Resolve already returned as live for some real,
// resolved call.
type Capability struct {
	live bool
}

// Live reports whether c grants pass-through (live) egress for the call
// it was resolved for.
func (c Capability) Live() bool { return c.live }

// Suppressed reports the opposite of Live -- named to match the ledger's
// own vocabulary (§30.6's suppressed_in_shadow outbox column) so a caller
// building a durable row can write, verbatim, something like
// `row.SuppressedInShadow = cap.Suppressed()` and have the field name and
// the value's own name agree. See doc.go's own "using this for the §30.8
// epoch stamp" section.
func (c Capability) Suppressed() bool { return !c.live }

// String renders c for logging -- never anything a caller should branch
// on (use Live/Suppressed for that).
func (c Capability) String() string {
	if c.live {
		return "live"
	}
	return "shadow"
}

// liveCapability is the ONLY function in this codebase that can produce a
// Capability whose Live() reports true. Unexported, and its callers are
// enumerated here because this is the file an auditor comes to for that
// answer:
//
//   - Resolve (resolve.go), whose every fail-closed path returns
//     shadowCapability() instead, never this.
//   - ResolvePlatform (resolve.go), which answers for artifacts naming no
//     repository at all, and therefore returns live whenever the
//     deployment-wide switch is off -- see its own doc comment for why
//     that default is deliberate rather than an oversight.
//
// A third caller belongs in this list. The list said "one caller" while
// there were two, which is the kind of stale enumeration that makes an
// auditor stop looking one branch too early.
func liveCapability() Capability {
	return Capability{live: true}
}

// shadowCapability is the explicit, named spelling of the zero value --
// used throughout Resolve for readability at every fail-closed return.
// Always identical to Capability{}.
func shadowCapability() Capability {
	return Capability{}
}
