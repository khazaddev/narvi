-- oidc_signing_keys: the control plane's own OIDC issuer signing-key
-- material (Step 73a, "cloud identity: OIDC issuer, bindings, minting",
-- §27.3). Every column matches §27.3's own literal shape: "oidc_signing_
-- keys(kid, private_key_encrypted, public_jwk, created_at, retired_at)".
--
-- kid is the table's own PRIMARY KEY, not a separate UUID id -- a JWT's
-- own "kid" header claim IS this row's natural key everywhere it is ever
-- looked up (JWKS publication, signing-key selection, token verification
-- by a cloud's own STS), so a second, redundant id column would only
-- invite the two drifting. kid is generated CP-side (a random,
-- unpredictable string -- internal/adapters/outbound/oidcsigning's own
-- key-generation adapter, §11: randomness belongs in an adapter, never
-- domain), never customer-chosen or derived from anything guessable.
--
-- private_key_encrypted is an RS256 RSA private key, PKCS8-DER-encoded
-- and then AES-256-GCM-encrypted via platform.EncryptToken -- the EXACT
-- same encryption-at-rest mechanism provider_credentials.value_encrypted/
-- sandbox_secrets.value_encrypted already use (§27.3: "private keys...
-- encrypted at rest with platform.EncryptToken"), reused directly rather
-- than a new crypto scheme invented for this Step. NEVER logged, NEVER
-- returned by any REST route -- this table has no management API surface
-- at all (rotation is a POST that returns metadata only, never the key
-- material); the only two things that ever decrypt it are the minting
-- endpoint (to sign a token) and nothing else.
--
-- public_jwk is the SAME key's own public half, pre-rendered as a single
-- JWK JSON object ({kty, n, e, kid, alg, use} -- RFC 7517) at generation
-- time, not derived from the private key on every JWKS request -- the
-- JWKS endpoint (a public, unauthenticated, high-traffic-by-design route
-- every configured cloud's own STS polls) does zero cryptographic work
-- per request, only a plain SELECT + a JSON array wrap.
--
-- retired_at NULL means this is the SINGLE currently-active signing key
-- (the one new tokens are minted with) -- enforced as an application-layer
-- invariant (internal/domain/oidckey), not a DB constraint, mirroring
-- sandbox_secrets' own "shape validated in Go, not duplicated as a second
-- CHECK" precedent for a comparably single-writer-path table. A non-NULL
-- retired_at means the key stopped signing NEW tokens at that instant but
-- keeps publishing in the JWKS response (and keeps verifying already-
-- minted tokens) until retired_at + platform.Timeouts.
-- CloudIdentitySigningKeyOverlapWindow -- the same overlapping-validity
-- discipline §5.2 already applies to sandbox-token rotation ("rotated on
-- identity rotation with a previous-gen grace window during overlapping
-- spawns"), applied here to signing keys instead of sandbox bearer
-- tokens. See internal/domain/oidckey's own doc comment for the full
-- rotation-trigger design (gap 2 of this Step's own brief).
CREATE TABLE oidc_signing_keys (
    kid                    TEXT PRIMARY KEY,
    private_key_encrypted  BYTEA NOT NULL,
    public_jwk             JSONB NOT NULL,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    retired_at             TIMESTAMPTZ
);

-- At most one currently-active (retired_at IS NULL) key at a time --
-- RotateSigningKeys always retires the previous active key in the SAME
-- transaction it inserts the new one (internal/adapters/outbound/postgres
-- .OIDCSigningKeyStore.Rotate), but this is cheap and load-bearing enough
-- (signing with two simultaneously "active" keys would be a real, silent
-- correctness bug -- a verifier would still accept either, but WHICH one
-- signs a given token would become nondeterministic) to also enforce as a
-- real DB constraint, not application discipline alone: a unique index on
-- a constant expression, partial to retired_at IS NULL, is Postgres' own
-- standard idiom for "at most one row may match this predicate" (there is
-- no NULL to collide on the way a plain UNIQUE(retired_at) would fail to
-- catch this -- every NULL is its own distinct value to a plain unique
-- constraint).
CREATE UNIQUE INDEX oidc_signing_keys_one_active_uniq ON oidc_signing_keys ((true)) WHERE retired_at IS NULL;

-- Lookup index for the JWKS endpoint's own "every key still inside its
-- own publish/verify window" query (retired_at IS NULL OR retired_at >
-- some cutoff) and for RotateSigningKeys' own "find the current active
-- key" read.
CREATE INDEX oidc_signing_keys_retired_at_idx ON oidc_signing_keys (retired_at);
