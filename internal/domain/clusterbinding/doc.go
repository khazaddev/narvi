// Package clusterbinding implements the pure vocabulary around Step 73b's
// own ("cloud identity: sandbox-side consumption + kubeconfig injection",
// §27.4) cluster_bindings table: the 3 recognized AuthKind values (in
// §27.4's own preference order, cloud > oidc > static), structural
// validation of a binding's own field combination (Validate), and the one
// fixed env-var name (KUBECONFIG) sandbox-agent's own kubeconfig-rendering
// mechanism sets on every spawned process. No I/O, no time.Now(), no
// randomness (§11) -- every function takes already-resolved values;
// rendering the actual kubeconfig YAML document and writing it to disk
// belong to cmd/sandbox-agent/kubeconfig.go.
//
// # v1 is environment-scoped only, no global fallback
//
// Unlike internal/domain/cloudidentity's own cloud_identity_bindings
// (environment OR global), a cluster_bindings row is ALWAYS
// environment-scoped -- §27.4's own model: "the target cluster... is
// selected the way §14 already models deployment targets: per-Environment
// ... one cluster per Environment in v1 (the bullet's own singular)".
// There is no "every Environment shares this cluster" concept to express,
// so this package carries no Scope type at all, unlike cloudidentity's own
// scope.go.
//
// # Auth rungs, and what each one needs
//
// The 3 AuthKind values are a PREFERENCE order, not independently ranked
// options a customer picks freely per binding -- each binding declares
// exactly one rung, and §27.4 documents which is preferred when more than
// one would technically work for a given cluster ("preferring federation
// over static material"):
//
//  1. AuthKindCloud -- rides §27.3's already-established cloud-identity
//     env vars via the target cloud's own standard exec-credential plugin
//     (`aws eks get-token` / `gke-gcloud-auth-plugin` / `kubelogin`).
//     Requires ServerURL/CABundle (Validate) to render the kubeconfig's
//     own `cluster` stanza, plus a `cloud` field in Params naming WHICH
//     of the three clouds (see this package's own params.go).
//  2. AuthKindOIDC -- a self-managed cluster whose kube-apiserver trusts
//     Narvi's own OIDC issuer directly; the kubeconfig authenticates via
//     its own `tokenFile` field (client-go's documented, periodically-
//     re-read mechanism), pointed at a token sandbox-agent mints and
//     refreshes itself -- NOT an exec plugin (an earlier version of this
//     rung used one, sandbox-agent's OWN `kube-credential` subcommand;
//     see cmd/sandbox-agent/kubeconfig.go's own top doc comment, "Design
//     correction", for why that shipped structurally non-functional and
//     was replaced). Also requires ServerURL/CABundle, plus a `clientId`
//     field in Params (the audience the token is minted for).
//  3. AuthKindStatic -- an uploaded, ALREADY-COMPLETE kubeconfig document
//     (its own server_url/ca_bundle baked in), stored as a §27.1
//     sandbox_secrets value and referenced by name via a `secretName`
//     field in Params. ServerURL/CABundle are NOT required for this rung
//     (Validate) -- this table's own copies would be redundant with what
//     the referenced document already carries, and are never rendered
//     into anything for this rung: the uploaded document is written to
//     disk VERBATIM, never templated (§27.4: "never env-var-expanded").
package clusterbinding
