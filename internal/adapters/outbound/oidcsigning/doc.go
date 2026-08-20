// Package oidcsigning implements §27.3's own ("cloud identity: OIDC
// issuer, bindings, minting", §27.3) RS256 key generation, JWK
// marshaling, and JWT sign/verify -- the one place in this codebase that
// touches RSA key material or randomness for this feature (§11: key
// generation and randomness belong in an adapter, never
// /internal/domain).
//
// # Standard library only, deliberately, matching platform.EncryptToken's
// own precedent
//
// No third-party JWT/JOSE library is added to go.mod for this -- the
// exact same "stdlib-first crypto convention" internal/platform/
// tokenencrypt.go's own doc comment already states for this codebase's
// AES-GCM helper ("crypto/aes + crypto/cipher + crypto/rand... no
// third-party crypto dependency added for this"). RS256 (RSASSA-PKCS1-v1_5
// using SHA-256, RFC 7518 §3.3) is directly expressible with crypto/rsa +
// crypto/x509 + crypto/sha256 + encoding/base64 -- a compact JWT is
// nothing more than base64url(header) + "." + base64url(payload) + "." +
// base64url(signature), and a JWK (RFC 7517) is nothing more than the
// RSA public key's own modulus/exponent, base64url-encoded, in a small
// fixed JSON object. Both are simple enough, and security-sensitive
// enough, that hand-rolling the SPECIFIC subset this Step needs (compact
// JWS with exactly one algorithm, one key type) is safer than pulling in
// a general-purpose JOSE library's much larger attack surface (algorithm
// confusion between "none"/HS*/RS*, JWK header injection, etc. -- classes
// of vulnerability that have repeatedly hit general JWT libraries in
// practice) for a feature that only ever needs to speak ONE specific,
// fixed shape to a small, known set of consumers (AWS/GCP/Azure STS).
//
// Sign/Verify below are that fixed subset ONLY: always RS256, always a
// 3-part compact-serialization JWT, never "alg": "none", never any
// algorithm the token's own header names other than RS256 -- Verify
// hardcodes RS256 rather than trusting the token's own header to say so,
// closing exactly the "algorithm confusion" class of bug named above.
package oidcsigning
