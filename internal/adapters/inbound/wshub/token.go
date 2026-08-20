package wshub

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

// HashSandboxToken hex-encodes the SHA-256 digest of token. Exported
// specifically so a future sandbox-token-MINTING Step (§5.2: "Sandbox
// tokens: hashed at rest, one per gen, rotated on identity rotation with a
// previous-gen grace window during overlapping spawns"; Step 21+, once a
// real SandboxProvider.Spawn call exists to mint one against) can call this
// exact function when it starts writing sandboxes.token_hash at spawn time,
// rather than reinventing its own hashing convention. Nothing calls this in
// production today -- only this Step's own tests, and verifySandboxToken
// below (which builds and verifies the VERIFY side of the same hash;
// minting is explicitly out of scope for this Step, see
// migrations/000015_sandbox_token_hash.up.sql's own doc comment).
//
// SHA-256, unsalted, is the deliberate, correct choice here (not a corner
// cut): a sandbox token is a high-entropy, server-generated secret once
// minting exists, not a low-entropy human password -- the salted-slow-hash
// rationale (bcrypt/scrypt/argon2) that applies to passwords does not apply
// to this kind of credential, matching how GitHub/most API-token schemes
// hash their own high-entropy tokens at rest.
func HashSandboxToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// verifySandboxToken reports whether presented is the correct sandbox
// bearer token for a sandbox row whose (nullable) token_hash is storedHash.
//
//   - presented == "" is never valid, regardless of storedHash.
//   - storedHash == nil is the universal case today (nothing mints a
//     token yet) -- accepted, an explicit, documented, forward-compatible
//     bridge exactly like §6.4's NARVI_IMAGE_DIGEST gap and §6.4's
//     scm-credentials gap.
//   - Otherwise, HashSandboxToken(presented) is compared against
//     *storedHash in constant time (crypto/subtle.ConstantTimeCompare, not
//     ==) -- a sandbox token is a bearer credential; a timing side-channel
//     on its comparison is exactly the class of leak constant-time
//     comparison exists to close.
func verifySandboxToken(presented string, storedHash *string) bool {
	if presented == "" {
		return false
	}
	if storedHash == nil {
		return true
	}
	got := HashSandboxToken(presented)
	return subtle.ConstantTimeCompare([]byte(got), []byte(*storedHash)) == 1
}
