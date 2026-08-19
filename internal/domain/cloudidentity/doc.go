// Package cloudidentity implements the pure vocabulary around Step 73a's
// own ("cloud identity: OIDC issuer, bindings, minting", §27.3)
// cloud_identity_bindings table and the claims a minted token carries: the
// 4 recognized Kind values, the 2 recognized binding Scope values (a
// deliberately narrower reuse of internal/domain/providercredential's own
// Scope type -- see ValidateScope's own doc comment), the kind/scope
// combination §27.3 forbids (ValidateBinding), the deterministic `sub`
// string a binding's own Environment maps to (Sub), and the pure claims
// shape a token carries (Claims/BuildClaims). No I/O, no time.Now(), no
// randomness (§11) -- every function takes already-resolved values;
// RSA signing, JWKS assembly, and the clock read all belong to
// internal/adapters/outbound/oidcsigning and the httpapi minting handler.
//
// # Gap 3 of this Step's own brief: global scope versus a per-Environment
// `sub`
//
// `sub` is always narvi:environment:<environment_id> (Sub, below) --
// never anything global or session-varying (§27.3: "sub is the one claim
// every cloud can condition on and Azure's federated-credential matching
// requires an exact, predictable subject string -- never anything
// session-varying in sub"). A binding may nonetheless be scope='global',
// meaning it applies to every Environment without an explicit
// environment-scoped override -- but the TOKEN a global binding's own
// audience grant produces still carries THAT session's own Environment's
// `sub`, never a wildcard or Environment-agnostic value. A single
// global-scope binding therefore asks one cloud role to trust MANY
// distinct `sub` values over its lifetime (one per Environment that ever
// mints against it), and the two clouds this codebase targets besides
// Azure can express that honestly in a single cloud-side policy: AWS IAM
// role trust policies support a StringLike condition with a wildcard
// against `sub` (e.g. "narvi:environment:*"); GCP workload-identity-pool
// attribute conditions are a CEL expression that can pattern-match a
// claim the same way. Azure federated credentials cannot -- §27.3 states
// this as the very reason `sub` is per-Environment in the first place
// ("Azure's federated-credential matching requires an exact, predictable
// subject string"), and Azure's own federated-credential subject field is
// documented as exact-match only, with no wildcard support at all. A
// global-scope Azure binding would therefore promise something Azure's
// own trust config cannot deliver in one entry -- silently: creating the
// binding would succeed, and the FIRST token minted against it for
// whichever Environment's federated credential the customer forgot to
// separately add would simply fail cloud-side, with an error Narvi never
// sees (this Step's own gap-1 theme, recurring here for a different
// reason).
//
// This package's answer, per the brief's own instruction ("either
// document the constraint plainly, or refuse the combination -- do not
// leave it implicit"): REFUSE the combination outright. ValidateBinding
// rejects KindAzure+ScopeGlobal unconditionally, at the exact same layer
// every other structural binding rule lives in, backed by a matching
// database CHECK constraint for defense in depth
// (cloud_identity_bindings_no_azure_global, migrations/
// 000093_cloud_identity_bindings.up.sql). aws/gcp/generic bindings MAY be
// global-scoped -- both real clouds this restriction targets can honestly
// express "trust every Environment" in one policy; azure bindings may
// not, and are never silently allowed to promise it.
package cloudidentity
