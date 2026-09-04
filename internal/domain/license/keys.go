// This file (keys.go) embeds the production issuer keyset. Public keys
// are public -- embedding them is the whole of "offline verification, no
// phone-home" (design note section 1.5) -- but the signing SEED that
// would let anyone mint a key this keyset accepts never exists in this
// repository, and never will: minting and key custody are the private
// repo's job (the design note's own "what tampering buys" section).

package license

import "crypto/ed25519"

// Keyset maps a key id ("kid") to the Ed25519 public key that verifies
// signatures minted under it. Rotation is additive: a new kid ships in a
// public release BEFORE any key is issued under it, and an old kid stays
// in this map for as long as a key issued under it might still be
// presented (the design note's own "rotation" section).
type Keyset map[string]ed25519.PublicKey

// productionKeys is the embedded production keyset.
//
// It is EMPTY today. No production signing key exists yet -- see this
// package's own doc comment for the "names are placeholders" decision
// this blocks on, and docs/design/boundaries-design.md, section 7 ("left
// to the repository owner"). An empty keyset is not a placeholder bug to fix
// before this ships: it is the correct fail-closed default. [Parse]
// looks up a grant's own "kid" claim in exactly this map, so with it
// empty EVERY key -- however well-formed, however validly signed by
// SOME Ed25519 key -- fails with [ErrUnknownKey], and every capability
// stays disabled (internal/app/capability.Registry.Enabled's own
// installed-AND-licensed-AND-valid-now conjunction can never reach
// "licensed" with no grant to check). Adding the first real entry here is
// a deliberate, reviewable PR in this repository, once a production
// signing key exists in the private repo's own custody; nothing outside
// this repository's own commit history can add to this map.
var productionKeys = Keyset{}

// IssuerKeys returns the embedded production keyset -- the public keys
// this build trusts to verify a [Parse] call's own signature against.
// Called once, at boot, by the composition root (controlplane).
//
// Returns a fresh copy, never productionKeys itself, so a caller cannot
// mutate this package's own trusted keyset by mutating the map it was
// handed -- cheap defensive copying that costs nothing while the map is
// empty and keeps mattering once it is not.
//
// Tests never call this: they use their own generated Ed25519 pair and a
// [Keyset] literal built from it (see [Parse]'s own tests), so a test
// fixture can never be mistaken for -- or accidentally validate against
// -- a real production key. [TestIssuerKeys_RejectTestSignedKeys] is the
// negative that pins this: a key signed by a test-generated pair must
// never verify against THIS function's own return value.
func IssuerKeys() Keyset {
	out := make(Keyset, len(productionKeys))
	for kid, pub := range productionKeys {
		out[kid] = pub
	}
	return out
}
