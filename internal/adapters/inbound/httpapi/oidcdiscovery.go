// This file (oidcdiscovery.go) implements Step 73a's own ("cloud
// identity: OIDC issuer, bindings, minting", §27.3) public issuer
// surface: GET /.well-known/openid-configuration and GET
// /.well-known/jwks.json.
//
// # This Step's own gap-1 resolution: PUBLIC, UNAUTHENTICATED, on purpose
//
// §27.3 never states this explicitly, but it is load-bearing: AWS, GCP,
// and Azure's own STS implementations fetch BOTH of these documents
// directly, over the public internet, with NO Narvi credential of any
// kind -- that is the entire point of the OIDC-federation pattern §27.3
// itself names as its precedent ("The pattern is the one GitHub Actions
// standardized for CI<->cloud federation... Narvi's control plane becomes
// an OIDC identity provider; the customer's cloud IAM is configured to
// trust it"). GitHub Actions' own real issuer
// (token.actions.githubusercontent.com) serves both documents completely
// unauthenticated for exactly this reason -- a cloud's STS has no
// mechanism to present ANY bearer credential when fetching issuer
// metadata, because doing so is the very capability being established,
// not a precondition of it. If these two routes were mounted behind
// auth.Middleware (a browser-session cookie check) the entire feature
// would be silently non-functional: every federation attempt would fail
// CLOUD-SIDE, with an error Narvi itself never observes (mirrors this
// same Step's own similarly-silent gap-3 failure mode for an
// azure+global binding) -- the worst kind of bug, because nothing in
// THIS codebase's own test suite would ever fail to catch it if these
// routes only had authenticated coverage.
//
// This codebase already has a well-established, repeatedly-used
// precedent for "mounted deliberately OUTSIDE auth.Middleware, with a
// comment saying so and why" -- scm-credentials, provider-credentials,
// sandbox-secrets, snapshot-mint, review/verdict, workflow/step-outcome,
// turn/epistemic-outcome, uploads, the Slack/GitHub webhook routes, and
// the identity-link consume route (cmd/control-plane/main.go, each with
// its own such comment). Every one of those is sandbox-bearer- or
// provider-signature-authenticated by SOME mechanism, just not a browser
// cookie -- these two routes are the FIRST in this codebase with no
// authentication mechanism at all, by design, following that SAME
// deliberate-exclusion precedent one step further: a route whose entire
// purpose is to be readable by an unauthenticated third party.
// TestOIDCDiscovery_Unauthenticated/TestOIDCJWKS_Unauthenticated (this
// file's own _test.go) PIN this: a request with literally no
// Authorization header, X-Sandbox-Gen header, or auth cookie of any kind
// must get 200 from both handlers.
//
// # Fail-closed when unset (§27.3's own explicit requirement)
//
// Both handlers respond 503 Service Unavailable when platform.Config.
// CloudIdentityIssuerURL is empty -- "the whole capability is off...
// when unset" (§27.3, verbatim), matching this codebase's own existing
// "feature not configured" precedent (uploadmint.go's own mintUploadCore,
// "uploads not configured" when ObjectStorage is nil) rather than
// inventing a second convention for the identical shape of gate. A real
// cloud STS never even attempts to fetch these unless a customer has
// already configured Narvi's issuer URL in their own trust policy, which
// requires the issuer URL to be non-empty in the first place -- there is
// no cloud-facing UX degradation from this choice, only an honest
// "not configured" signal for anyone who does probe it directly.

package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/platform"
)

// jwksPath is the fixed path segment platform.Config.CloudIdentityIssuerURL
// is joined with to form the discovery document's own jwks_uri --
// mounted at this SAME literal path by cmd/control-plane/main.go's own
// router.Get call (kept as one named constant so the two can never drift
// apart).
const jwksPath = "/.well-known/jwks.json"

// oidcDiscoveryDocument is the minimal OIDC Provider Metadata document
// (OpenID Connect Discovery 1.0) this control plane serves -- the SAME
// machine-to-machine, token-only issuer shape GitHub Actions' own OIDC
// provider uses for CI<->cloud federation (§27.3's own stated precedent).
// Deliberately has NO authorization_endpoint/token_endpoint field at all:
// this issuer never runs an interactive login flow of any kind --
// id_token minting happens entirely through POST /sessions/{id}/
// cloud-identity-token instead (cloudidentitytoken.go), a Narvi-specific,
// sandbox-bearer-authenticated endpoint no generic OIDC client (a cloud's
// STS included) ever calls directly, so it has no standard metadata field
// to advertise in the first place -- omitting those two fields is the
// honest description of what this issuer actually does, not an
// oversight.
type oidcDiscoveryDocument struct {
	Issuer                           string   `json:"issuer"`
	JWKSURI                          string   `json:"jwks_uri"`
	ResponseTypesSupported           []string `json:"response_types_supported"`
	SubjectTypesSupported            []string `json:"subject_types_supported"`
	IDTokenSigningAlgValuesSupported []string `json:"id_token_signing_alg_values_supported"`
	ClaimsSupported                  []string `json:"claims_supported"`
	ScopesSupported                  []string `json:"scopes_supported"`
}

// OIDCDiscovery backs GET /.well-known/openid-configuration -- see this
// file's own top doc comment for the full public/unauthenticated/
// fail-closed design. issuerURL is platform.Config.CloudIdentityIssuerURL,
// passed by value at server-construction time (cmd/control-plane/main.go)
// -- empty means the capability is off.
func OIDCDiscovery(issuerURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if issuerURL == "" {
			writeError(w, http.StatusServiceUnavailable, "cloud identity federation is not configured")
			return
		}
		doc := oidcDiscoveryDocument{
			Issuer:                           issuerURL,
			JWKSURI:                          issuerURL + jwksPath,
			ResponseTypesSupported:           []string{"id_token"},
			SubjectTypesSupported:            []string{"public"},
			IDTokenSigningAlgValuesSupported: []string{"RS256"},
			ClaimsSupported:                  []string{"iss", "sub", "aud", "exp", "iat", "session_id", "gen", "repos", "provenance_tag"},
			ScopesSupported:                  []string{"openid"},
		}
		writeJSON(w, http.StatusOK, doc)
	}
}

// jwksResponse is the standard {"keys": [...]} JWKS document (RFC 7517
// §5) -- each element is a stored oidc_signing_keys.public_jwk value,
// wrapped as a raw JSON message (never decoded into a Go struct and
// re-marshaled: zero cryptographic or even structural work per request,
// see migrations/000092_oidc_signing_keys.up.sql's own doc comment on
// why public_jwk is pre-rendered once, at generation time).
type jwksResponse struct {
	Keys []json.RawMessage `json:"keys"`
}

// OIDCJWKS backs GET /.well-known/jwks.json -- see this file's own top
// doc comment for the full public/unauthenticated/fail-closed design.
// Publishes every signing key still inside its own overlap window
// (internal/domain/oidckey.IsPublishable): the currently-active key plus
// any key retired more recently than
// timeouts.CloudIdentitySigningKeyOverlapWindow ago -- the overlapping-
// validity discipline §5.2 already establishes for sandbox-token
// rotation, applied here (§27.3). Reads the wall clock directly
// (time.Now()) rather than through an injected Clock interface --
// httpapi is an inbound ADAPTER, not /internal/domain, so §11's
// domain-purity rule does not apply here; mirrors wstoken.go's own
// MintWSToken, which reads time.Now() the identical way. The rotation-
// overlap-window behavior itself is fully covered without mocking this
// handler's own clock: postgres.OIDCSigningKeyStore.Rotate/
// ListPublishable both take an explicit `now` parameter, so a test can
// retire a key at a controlled, arbitrary instant in the past and then
// call this REAL handler (real clock) to observe the SAME publish-window
// arithmetic a genuine wall-clock rotation would eventually produce --
// see cloudidentitytoken_integration_test.go's own rotation test.
func OIDCJWKS(signingKeys *postgres.OIDCSigningKeyStore, issuerURL string, timeouts platform.Timeouts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if issuerURL == "" {
			writeError(w, http.StatusServiceUnavailable, "cloud identity federation is not configured")
			return
		}
		ctx := r.Context()
		logger := platform.Logger(ctx)

		rows, err := signingKeys.ListPublishable(ctx, time.Now(), timeouts.CloudIdentitySigningKeyOverlapWindow)
		if err != nil {
			logger.Error("httpapi: jwks: list publishable signing keys failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		keys := make([]json.RawMessage, 0, len(rows))
		for _, row := range rows {
			keys = append(keys, json.RawMessage(row.PublicJwk))
		}
		writeJSON(w, http.StatusOK, jwksResponse{Keys: keys})
	}
}
