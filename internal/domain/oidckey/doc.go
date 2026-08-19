// Package oidckey implements the pure vocabulary around Step 73a's own
// ("cloud identity: OIDC issuer, bindings, minting", §27.3) OIDC
// signing-key rotation: which of a set of already-fetched
// oidc_signing_keys rows is the one new tokens should be signed with, and
// which of them are still inside their own JWKS-publish/token-verify
// window. No I/O, no time.Now(), no randomness (§11) -- every function
// here takes an already-resolved `now time.Time` and already-fetched key
// rows; RSA key generation, the clock read, and the actual Postgres
// read/write all belong to internal/adapters/outbound/oidcsigning and
// internal/adapters/outbound/postgres.OIDCSigningKeyStore respectively.
//
// # Gap 2 of this Step's own brief: what triggers rotation
//
// §27.3 specifies rotation's SHAPE ("rotation publishes old + new in the
// JWKS for an overlap window >= max token lifetime -- the same
// overlapping-validity discipline §5.2 already applies to sandbox-token
// rotation") but never states what INITIATES a rotation. §27.8 (this
// same technical plan's own "risks and open questions" section for §27.3)
// resolves this explicitly, in the plan's own words: "Key-rotation
// cadence (§27.3): manual, admin-triggered rotation with the overlap
// window is v1; automatic scheduled rotation is deferred until
// operational experience says what cadence is right." This package, and
// the admin-only POST /api/cloud-identity/signing-keys/rotate endpoint it
// backs (internal/adapters/inbound/httpapi/cloudidentitykeys.go), build
// exactly that: a single admin-triggered action, gated by
// authz.ActionManageCloudIdentityKeys (row 6, §13.3 -- admin only, the
// same row "integrations/global secrets" already occupies, since a
// signing-key rotation is a platform-wide security posture change, not
// scoped to one Environment the way binding CRUD is), with NO age-
// threshold or scheduled-job trigger of any kind. This directly mirrors
// §5.2's own sandbox-token precedent this section explicitly cites:
// "Sandbox tokens: hashed at rest, one per gen, rotated ON IDENTITY
// ROTATION [a discrete, triggered event -- a new sandbox gen being
// spawned, not a timer] with a previous-gen grace window during
// overlapping spawns." Neither mechanism in this codebase rotates a
// credential on a schedule; both rotate on a discrete triggering event
// (a new gen for sandbox tokens, an admin's own POST for signing keys)
// and both then hold the OLD credential valid for a fixed grace/overlap
// window so anything already relying on it does not break mid-flight.
//
// The overlap window itself is platform.Timeouts.
// CloudIdentitySigningKeyOverlapWindow (internal/platform/timeouts.go,
// §5.4/§11's own "every timeout lives in one file" rule) -- 15 minutes by
// default, comfortably above platform.Timeouts.CloudIdentityTokenLifetime
// (10 minutes, §27.3's own "exp ~= 10 minutes") with the same explicit
// margin timeouts.Validate() enforces on every other pairwise link in
// that file.
//
// # What "publishable" means
//
// A key is publishable in the JWKS response (and therefore still able to
// verify a token signed under it) from the moment it is created until
// CloudIdentitySigningKeyOverlapWindow after it is retired -- see
// IsPublishable. Exactly one key is ever the SIGNING key at a time: the
// one with a nil RetiredAt (see IsActive) -- RotateSigningKeys
// (internal/adapters/outbound/postgres.OIDCSigningKeyStore.Rotate)
// retires the previous active key and creates a new one, atomically, in
// one transaction, so there is never a moment with zero or two active
// keys once at least one rotation has ever run. Before the first
// rotation ever runs, there is no active key at all -- minting and JWKS
// both degrade honestly (mint fails closed with "no active signing key
// configured"; JWKS returns an empty `{"keys": []}`, a valid, if useless,
// document) rather than crashing.
package oidckey
