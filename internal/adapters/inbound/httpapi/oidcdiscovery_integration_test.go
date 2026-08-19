//go:build integration

// Integration tests for Step 73a's own ("cloud identity: OIDC issuer,
// bindings, minting", §27.3) public discovery/JWKS endpoints
// (oidcdiscovery.go), against a real Postgres instance -- sharing this
// package's own testRig (httpapi_integration_test.go).
package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/oidcsigning"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// discoveryDocument mirrors internal/adapters/inbound/httpapi's own
// unexported oidcDiscoveryDocument for this test's own decode target.
type discoveryDocument struct {
	Issuer                           string   `json:"issuer"`
	JWKSURI                          string   `json:"jwks_uri"`
	ResponseTypesSupported           []string `json:"response_types_supported"`
	SubjectTypesSupported            []string `json:"subject_types_supported"`
	IDTokenSigningAlgValuesSupported []string `json:"id_token_signing_alg_values_supported"`
	ClaimsSupported                  []string `json:"claims_supported"`
	ScopesSupported                  []string `json:"scopes_supported"`
}

// jwksDoc mirrors internal/adapters/inbound/httpapi's own unexported
// jwksResponse for this test's own decode target.
type jwksDoc struct {
	Keys []oidcsigning.JWK `json:"keys"`
}

// TestOIDCDiscovery_Unauthenticated is this Step's own gap-1 pinning
// test: a request carrying NO Authorization header, NO X-Sandbox-Gen
// header, and NO auth cookie of any kind must get 200. token="" to
// doJSON means "attach no cookie at all" (that helper's own doc comment)
// -- this is the SAME as a real, unmodified cloud STS request, which can
// never present any Narvi credential at all (oidcdiscovery.go's own top
// doc comment has the full "why" this route exists this way).
//
// Mutation test (run manually during verification, per this Step's own
// brief -- reverted immediately after, byte-identical): temporarily wrap
// this route's own mount below (and cmd/control-plane/main.go's real
// one) inside an r.Use(auth.Middleware(...)) block, mirroring every OTHER
// route group in this rig -- this test must then fail with 401, proving
// the test actually exercises the route mounting decision, not merely
// the handler function in isolation.
func TestOIDCDiscovery_Unauthenticated(t *testing.T) {
	rig := newTestRig(t)

	var doc discoveryDocument
	status := rig.doJSON(t, http.MethodGet, "/.well-known/openid-configuration", nil, &doc, "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (no credential presented at all)", status, http.StatusOK)
	}
	if doc.Issuer != rig.cloudIdentityIssuerURL {
		t.Errorf("Issuer = %q, want %q", doc.Issuer, rig.cloudIdentityIssuerURL)
	}
	if doc.JWKSURI != rig.cloudIdentityIssuerURL+"/.well-known/jwks.json" {
		t.Errorf("JWKSURI = %q, want %q", doc.JWKSURI, rig.cloudIdentityIssuerURL+"/.well-known/jwks.json")
	}
	if len(doc.IDTokenSigningAlgValuesSupported) != 1 || doc.IDTokenSigningAlgValuesSupported[0] != "RS256" {
		t.Errorf("IDTokenSigningAlgValuesSupported = %v, want [RS256]", doc.IDTokenSigningAlgValuesSupported)
	}
}

// TestOIDCJWKS_Unauthenticated is the JWKS half of the identical gap-1
// pin -- see TestOIDCDiscovery_Unauthenticated's own doc comment.
func TestOIDCJWKS_Unauthenticated(t *testing.T) {
	rig := newTestRig(t)

	var doc jwksDoc
	status := rig.doJSON(t, http.MethodGet, "/.well-known/jwks.json", nil, &doc, "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (no credential presented at all)", status, http.StatusOK)
	}
	// Empty is a valid, expected state before any rotation has ever run
	// (migrations/000092_oidc_signing_keys.up.sql's own doc comment) --
	// this test only proves the ROUTE is reachable unauthenticated, not
	// its content; TestOIDCJWKS_PublishesActiveKey (below) proves content.
	if doc.Keys == nil {
		t.Errorf(`Keys = nil, want a (possibly empty) non-nil slice from a well-formed {"keys":[]} response`)
	}
}

// TestOIDCDiscovery_IssuerUnset_FailsClosed proves the capability-off
// gate: an empty CloudIdentityIssuerURL must 503, never crash, never
// silently serve a document naming an empty issuer.
func TestOIDCDiscovery_IssuerUnset_FailsClosed(t *testing.T) {
	rig := newTestRig(t, func(r *testRig) { r.cloudIdentityIssuerURL = "" })

	status := rig.doJSON(t, http.MethodGet, "/.well-known/openid-configuration", nil, nil, "")
	if status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", status, http.StatusServiceUnavailable)
	}
}

// TestOIDCJWKS_IssuerUnset_FailsClosed is the JWKS half of the identical
// capability-off gate.
func TestOIDCJWKS_IssuerUnset_FailsClosed(t *testing.T) {
	rig := newTestRig(t, func(r *testRig) { r.cloudIdentityIssuerURL = "" })

	status := rig.doJSON(t, http.MethodGet, "/.well-known/jwks.json", nil, nil, "")
	if status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", status, http.StatusServiceUnavailable)
	}
}

// TestOIDCJWKS_PublishesActiveKey proves the JWKS endpoint actually
// republishes a real, rotated-in signing key's own public_jwk verbatim --
// via the REAL admin rotation endpoint (RotateCloudIdentitySigningKey),
// not a direct store write, so this test also exercises the full
// generate -> encrypt -> store -> publish pipeline end to end.
func TestOIDCJWKS_PublishesActiveKey(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	var rotateResp restdtos.RotateCloudIdentitySigningKeyResponse
	status := rig.doJSON(t, http.MethodPost, "/api/cloud-identity/signing-keys/rotate", []byte(`{}`), &rotateResp, token)
	if status != http.StatusOK {
		t.Fatalf("rotate status = %d, want %d", status, http.StatusOK)
	}

	var doc jwksDoc
	status = rig.doJSON(t, http.MethodGet, "/.well-known/jwks.json", nil, &doc, "")
	if status != http.StatusOK {
		t.Fatalf("jwks status = %d, want %d", status, http.StatusOK)
	}
	if len(doc.Keys) != 1 {
		t.Fatalf("len(doc.Keys) = %d, want 1", len(doc.Keys))
	}
	if doc.Keys[0].Kid != rotateResp.ActiveKid {
		t.Errorf("published kid = %q, want %q", doc.Keys[0].Kid, rotateResp.ActiveKid)
	}
	if doc.Keys[0].Kty != "RSA" || doc.Keys[0].Alg != "RS256" || doc.Keys[0].Use != "sig" {
		t.Errorf("published JWK = %+v, unexpected fixed fields", doc.Keys[0])
	}
	if doc.Keys[0].N == "" || doc.Keys[0].E == "" {
		t.Errorf("published JWK missing n/e: %+v", doc.Keys[0])
	}
}
