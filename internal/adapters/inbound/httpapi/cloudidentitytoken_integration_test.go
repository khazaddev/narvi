//go:build integration

// Integration tests for §27.3's own ("cloud identity: OIDC issuer,
// bindings, minting", §27.3) sandbox-facing minting endpoint
// (cloudidentitytoken.go), against a real Postgres instance -- sharing
// this package's own testRig (httpapi_integration_test.go) and
// scmcredentials_integration_test.go's own createSandboxWithToken/
// moveSandboxStatus/bumpSandboxGen helpers (same package, same auth/dead-
// sandbox/gen-fencing shape being proven here) and
// providercredentialsdelivery_integration_test.go's own
// createSessionWithReposAndEnvironment/providerCredsRepos.
package httpapi_test

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/oidcsigning"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
)

// mintTokenResponse mirrors internal/adapters/inbound/httpapi's own
// (unexported) mintCloudIdentityTokenResponse for this test's own decode
// target -- same "two independently-maintained wire types, reconciled by
// hand" convention providerCredsResponse already establishes in this
// package (providercredentialsdelivery_integration_test.go).
type mintTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// cloudIdentityClaims mirrors internal/domain/cloudidentity.Claims for
// this test's own decode target, after independently verifying the
// token's own signature (never trusting a claim before the signature
// check passes).
type cloudIdentityClaims struct {
	Issuer        string   `json:"iss"`
	Subject       string   `json:"sub"`
	Audience      string   `json:"aud"`
	IssuedAt      int64    `json:"iat"`
	Expiry        int64    `json:"exp"`
	SessionID     string   `json:"session_id"`
	Gen           int64    `json:"gen"`
	Repos         []string `json:"repos"`
	ProvenanceTag *string  `json:"provenance_tag"`
}

// postCloudIdentityToken posts to the real minting route with the given
// bearer/gen/body -- mirrors postProviderCredentials' own identical
// shape (providercredentialsdelivery_integration_test.go), extended with
// a real JSON body (the requested audience) that endpoint does not have.
func postCloudIdentityToken(t *testing.T, r testRig, sessionID, bearer, gen, body string) (int, mintTokenResponse) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, r.server.URL+"/sessions/"+sessionID+"/cloud-identity-token", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if gen != "" {
		req.Header.Set("X-Sandbox-Gen", gen)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var got mintTokenResponse
	_ = json.NewDecoder(resp.Body).Decode(&got)
	return resp.StatusCode, got
}

// createEnvironmentBindingViaAPI is a thin wrapper creating a real
// environment-scoped cloud_identity_bindings row through the REAL admin
// REST endpoint (never a direct store write) -- so every minting test
// below exercises the full "an admin configures a binding, a sandbox
// mints against it" pipeline, not a shortcut.
func createEnvironmentBindingViaAPI(t *testing.T, r testRig, environmentID, kind, audience string) {
	t.Helper()
	_, token := createUserWithRole(context.Background(), t, r, sqlcgen.UserRoleMaintainer)
	status := r.doJSON(t, http.MethodPost, "/api/environments/"+environmentID+"/cloud-identity-bindings",
		[]byte(`{"kind":"`+kind+`","audience":"`+audience+`"}`), nil, token)
	if status != http.StatusCreated {
		t.Fatalf("create environment binding: status = %d, want %d", status, http.StatusCreated)
	}
}

func createGlobalBindingViaAPI(t *testing.T, r testRig, kind, audience string) {
	t.Helper()
	_, token := createUserWithRole(context.Background(), t, r, sqlcgen.UserRoleMaintainer)
	status := r.doJSON(t, http.MethodPost, "/api/cloud-identity-bindings",
		[]byte(`{"kind":"`+kind+`","audience":"`+audience+`"}`), nil, token)
	if status != http.StatusCreated {
		t.Fatalf("create global binding: status = %d, want %d", status, http.StatusCreated)
	}
}

func rotateSigningKeyViaAPI(t *testing.T, r testRig) restdtos.RotateCloudIdentitySigningKeyResponse {
	t.Helper()
	_, token := createUserWithRole(context.Background(), t, r, sqlcgen.UserRoleAdmin)
	var got restdtos.RotateCloudIdentitySigningKeyResponse
	status := r.doJSON(t, http.MethodPost, "/api/cloud-identity/signing-keys/rotate", []byte(`{}`), &got, token)
	if status != http.StatusOK {
		t.Fatalf("rotate: status = %d, want %d", status, http.StatusOK)
	}
	return got
}

// fetchJWKS fetches the real, unauthenticated JWKS document.
func fetchJWKS(t *testing.T, r testRig) []oidcsigning.JWK {
	t.Helper()
	var doc jwksDoc
	status := r.doJSON(t, http.MethodGet, "/.well-known/jwks.json", nil, &doc, "")
	if status != http.StatusOK {
		t.Fatalf("fetch jwks: status = %d, want %d", status, http.StatusOK)
	}
	return doc.Keys
}

// --- Auth / dead-sandbox / gen-fencing (mirrors providercredentialsdelivery_integration_test.go) ---

func TestMintCloudIdentityToken_MissingBearer(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, true)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	status, _ := postCloudIdentityToken(t, rig, session.ID.String(), "", "1", `{"audience":"sts.amazonaws.com"}`)
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

func TestMintCloudIdentityToken_InvalidBearer(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, true)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	status, _ := postCloudIdentityToken(t, rig, session.ID.String(), "totally-wrong-token", "1", `{"audience":"sts.amazonaws.com"}`)
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

func TestMintCloudIdentityToken_UnknownSession(t *testing.T) {
	rig := newTestRig(t)
	status, _ := postCloudIdentityToken(t, rig, "11111111-1111-1111-1111-111111111111", "any-token", "1", `{"audience":"sts.amazonaws.com"}`)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

// TestMintCloudIdentityToken_DeadSandbox_MutationPin: remove the
// sandbox.IsDeadSandboxStatus check (or its ordering) inside
// MintCloudIdentityToken and this test must fail. §27.3's own explicit
// requirement: "minting stops at dead-sandbox/410, like every other
// delivery endpoint."
func TestMintCloudIdentityToken_DeadSandbox_MutationPin(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, true)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")
	moveSandboxStatus(ctx, t, rig, session.ID, sqlcgen.SandboxStatusStopped)

	status, _ := postCloudIdentityToken(t, rig, session.ID.String(), "sandbox-bearer-token", "1", `{"audience":"sts.amazonaws.com"}`)
	if status != http.StatusGone {
		t.Errorf("status = %d, want %d (dead sandbox)", status, http.StatusGone)
	}
}

// TestMintCloudIdentityToken_GenMismatch_MutationPin: remove or weaken
// the X-Sandbox-Gen fencing inside MintCloudIdentityToken and this test
// must fail. A valid binding + an active signing key are deliberately
// configured here (unlike a bare-minimum gen-mismatch test) so that a
// BYPASSED gen check would fall all the way through to a 200 success --
// not merely a DIFFERENT 403 for an unrelated reason (e.g. "no binding
// configured"), which would let the gen check regress silently while
// this test kept passing for the wrong reason.
func TestMintCloudIdentityToken_GenMismatch_MutationPin(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, true)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")
	createEnvironmentBindingViaAPI(t, rig, session.EnvironmentID.String(), "aws", "sts.amazonaws.com")
	rotateSigningKeyViaAPI(t, rig)

	status, _ := postCloudIdentityToken(t, rig, session.ID.String(), "sandbox-bearer-token", "999", `{"audience":"sts.amazonaws.com"}`)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d (gen mismatch)", status, http.StatusForbidden)
	}
}

// --- Capability gate ---

func TestMintCloudIdentityToken_IssuerUnset_FailsClosed(t *testing.T) {
	rig := newTestRig(t, func(r *testRig) { r.cloudIdentityIssuerURL = "" })
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, true)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	status, _ := postCloudIdentityToken(t, rig, session.ID.String(), "sandbox-bearer-token", "1", `{"audience":"sts.amazonaws.com"}`)
	if status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", status, http.StatusServiceUnavailable)
	}
}

// --- Request validation ---

func TestMintCloudIdentityToken_MalformedBody(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, true)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	status, _ := postCloudIdentityToken(t, rig, session.ID.String(), "sandbox-bearer-token", "1", `not json`)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

func TestMintCloudIdentityToken_BlankAudience(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, true)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	status, _ := postCloudIdentityToken(t, rig, session.ID.String(), "sandbox-bearer-token", "1", `{"audience":""}`)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

// --- Environment-less session refused ---

func TestMintCloudIdentityToken_NoEnvironment_Refused(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, false) // no Environment.
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")
	createGlobalBindingViaAPI(t, rig, "aws", "sts.amazonaws.com")

	status, _ := postCloudIdentityToken(t, rig, session.ID.String(), "sandbox-bearer-token", "1", `{"audience":"sts.amazonaws.com"}`)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d (a session with no Environment must be refused, even against a matching global binding)", status, http.StatusForbidden)
	}
}

// --- The audience allowlist check (this Step's own central security
// guard, §27.3: "CP refuses any audience no binding... declares") ---

// TestMintCloudIdentityToken_AudienceNotDeclared_MutationPin mutation-tests
// the audience allowlist: remove resolveCloudIdentityBindingForAudience's
// own call (or its own 403-on-!matched branch) inside MintCloudIdentityToken
// and this test must fail.
func TestMintCloudIdentityToken_AudienceNotDeclared_MutationPin(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, true)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")
	createEnvironmentBindingViaAPI(t, rig, session.EnvironmentID.String(), "aws", "sts.amazonaws.com")
	rotateSigningKeyViaAPI(t, rig)

	// A DIFFERENT audience than the one bound -- no binding declares it.
	status, _ := postCloudIdentityToken(t, rig, session.ID.String(), "sandbox-bearer-token", "1", `{"audience":"some-other-audience.example.test"}`)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d (no binding declares this audience)", status, http.StatusForbidden)
	}
}

func TestMintCloudIdentityToken_NoBindingAtAll_Refused(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, true)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")
	rotateSigningKeyViaAPI(t, rig)

	status, _ := postCloudIdentityToken(t, rig, session.ID.String(), "sandbox-bearer-token", "1", `{"audience":"sts.amazonaws.com"}`)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

// --- No active signing key ---

func TestMintCloudIdentityToken_NoActiveSigningKey_FailsClosed(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, true)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")
	createEnvironmentBindingViaAPI(t, rig, session.EnvironmentID.String(), "aws", "sts.amazonaws.com")
	// Deliberately no rotation call -- no signing key exists yet.

	status, _ := postCloudIdentityToken(t, rig, session.ID.String(), "sandbox-bearer-token", "1", `{"audience":"sts.amazonaws.com"}`)
	if status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d (no active signing key configured)", status, http.StatusServiceUnavailable)
	}
}

// --- Global fallback ---

func TestMintCloudIdentityToken_GlobalBindingFallback_Succeeds(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, true)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")
	createGlobalBindingViaAPI(t, rig, "aws", "sts.amazonaws.com") // no environment-scoped binding at all.
	rotateSigningKeyViaAPI(t, rig)

	status, resp := postCloudIdentityToken(t, rig, session.ID.String(), "sandbox-bearer-token", "1", `{"audience":"sts.amazonaws.com"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (global binding should apply as a fallback)", status, http.StatusOK)
	}
	if resp.Token == "" {
		t.Error("Token is empty")
	}
}

// --- Observability: mint success log line (§27.3: "Minting is logged
// with correlation_id (§5.3) and counted as a metric") ---

// TestMintCloudIdentityToken_HappyPath_LogsMintSuccessLine pins the
// success-path log line an adversarial review found entirely missing:
// every `logger` call inside MintCloudIdentityToken was on a
// refusal/failure branch that returns immediately, so a successful mint
// -- the ONLY per-mint event §27.3 asks to be logged at all (audit_log
// deliberately excludes each 5-minute refresh) -- produced no log output
// whatsoever. Uses captureDefaultLoggerJSON/findLogEntry
// (planapprove_integration_test.go's own established convention, same
// package). Mutation test (run manually during verification, reverted
// immediately after, byte-identical): deleting the logger.Info call
// inside MintCloudIdentityToken's own success tail (cloudidentitytoken.go)
// must make this test fail.
func TestMintCloudIdentityToken_HappyPath_LogsMintSuccessLine(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, true)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")
	createEnvironmentBindingViaAPI(t, rig, session.EnvironmentID.String(), "aws", "sts.amazonaws.com")
	rotated := rotateSigningKeyViaAPI(t, rig)

	buf := captureDefaultLoggerJSON(t)

	status, resp := postCloudIdentityToken(t, rig, session.ID.String(), "sandbox-bearer-token", "1", `{"audience":"sts.amazonaws.com"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if resp.Token == "" {
		t.Fatal("Token is empty")
	}

	entry := findLogEntry(t, buf, "httpapi: cloud-identity-token: minted")
	if got, _ := entry["session_id"].(string); got != session.ID.String() {
		t.Errorf("session_id = %q, want %q", got, session.ID.String())
	}
	if got, _ := entry["environment_id"].(string); got != session.EnvironmentID.String() {
		t.Errorf("environment_id = %q, want %q", got, session.EnvironmentID.String())
	}
	if got, _ := entry["audience"].(string); got != "sts.amazonaws.com" {
		t.Errorf("audience = %q, want %q", got, "sts.amazonaws.com")
	}
	if got, _ := entry["kid"].(string); got != rotated.ActiveKid {
		t.Errorf("kid = %q, want %q", got, rotated.ActiveKid)
	}
	if _, ok := entry["expires_at"]; !ok {
		t.Error("expires_at missing from log entry")
	}
	// NEVER the token itself, anywhere in the log line -- matches this
	// handler's own "never logs plaintext/ciphertext" discipline for the
	// signing key material.
	for k, v := range entry {
		if s, ok := v.(string); ok && strings.Contains(s, resp.Token) {
			t.Errorf("log entry field %q contains the minted token verbatim: %q", k, s)
		}
	}
}

// --- End-to-end crypto proof: mint a real token, fetch the real JWKS,
// verify with a standard library path, decode claims ---

// TestMintCloudIdentityToken_EndToEnd_RealSignatureVerification is this
// Step's own "prove the crypto works end to end rather than asserting
// it" requirement, run through the REAL HTTP endpoints (never a direct
// store/adapter call): an admin creates a binding and rotates a signing
// key through the real REST routes, a sandbox mints a real token through
// the real minting route, the test fetches the REAL, unauthenticated
// JWKS document, reconstructs the public key from the published JWK
// (never the original key material -- this test never has access to
// that, exactly like a real cloud STS), and verifies the signature via
// rsa.VerifyPKCS1v15 directly (the real standard-library primitive) --
// not merely through oidcsigning.Verify, so a bug that made THAT wrapper
// report success unconditionally would still be caught here.
func TestMintCloudIdentityToken_EndToEnd_RealSignatureVerification(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, true)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")
	createEnvironmentBindingViaAPI(t, rig, session.EnvironmentID.String(), "aws", "sts.amazonaws.com")
	rotated := rotateSigningKeyViaAPI(t, rig)

	tag := "prototyping-test"
	if _, err := rig.pool.Exec(ctx, `UPDATE sessions SET provenance_tag = $2 WHERE id = $1`, session.ID, tag); err != nil {
		t.Fatalf("set provenance_tag: %v", err)
	}

	before := time.Now()
	status, resp := postCloudIdentityToken(t, rig, session.ID.String(), "sandbox-bearer-token", "1", `{"audience":"sts.amazonaws.com"}`)
	after := time.Now()
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if resp.Token == "" {
		t.Fatal("Token is empty")
	}

	parts := strings.Split(resp.Token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d dot-separated parts, want 3", len(parts))
	}

	gotKid, err := oidcsigning.ExtractKid(resp.Token)
	if err != nil {
		t.Fatalf("ExtractKid: %v", err)
	}
	if gotKid != rotated.ActiveKid {
		t.Errorf("token kid = %q, want %q (the currently active signing key)", gotKid, rotated.ActiveKid)
	}

	keys := fetchJWKS(t, rig)
	var matched *oidcsigning.JWK
	for i := range keys {
		if keys[i].Kid == gotKid {
			matched = &keys[i]
		}
	}
	if matched == nil {
		t.Fatalf("no published JWK matches token's own kid %q", gotKid)
	}

	pub, err := oidcsigning.PublicKeyFromJWK(*matched)
	if err != nil {
		t.Fatalf("PublicKeyFromJWK: %v", err)
	}

	// Independent, direct standard-library re-verification.
	headerAndPayload := parts[0] + "." + parts[1]
	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	hashed := sha256.Sum256([]byte(headerAndPayload))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, hashed[:], sigBytes); err != nil {
		t.Fatalf("independent rsa.VerifyPKCS1v15 failed: %v", err)
	}

	// Also verify via the package's own Verify wrapper, to prove the two
	// paths agree.
	payload, err := oidcsigning.Verify(resp.Token, pub)
	if err != nil {
		t.Fatalf("oidcsigning.Verify: %v", err)
	}

	var claims cloudIdentityClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}

	wantSub := "narvi:environment:" + session.EnvironmentID.String()
	if claims.Subject != wantSub {
		t.Errorf("Subject = %q, want %q", claims.Subject, wantSub)
	}
	if claims.Audience != "sts.amazonaws.com" {
		t.Errorf("Audience = %q, want sts.amazonaws.com", claims.Audience)
	}
	if claims.Issuer != rig.cloudIdentityIssuerURL {
		t.Errorf("Issuer = %q, want %q", claims.Issuer, rig.cloudIdentityIssuerURL)
	}
	if claims.SessionID != session.ID.String() {
		t.Errorf("SessionID = %q, want %q", claims.SessionID, session.ID.String())
	}
	if claims.Gen != 1 {
		t.Errorf("Gen = %d, want 1", claims.Gen)
	}
	if len(claims.Repos) != 1 || claims.Repos[0] != "acme/widgets" {
		t.Errorf("Repos = %v, want [acme/widgets]", claims.Repos)
	}
	if claims.ProvenanceTag == nil || *claims.ProvenanceTag != tag {
		t.Errorf("ProvenanceTag = %v, want %q", claims.ProvenanceTag, tag)
	}

	wantLifetime := platform.DefaultTimeouts().CloudIdentityTokenLifetime
	gotLifetime := time.Duration(claims.Expiry-claims.IssuedAt) * time.Second
	if gotLifetime != wantLifetime {
		t.Errorf("exp - iat = %v, want %v", gotLifetime, wantLifetime)
	}
	if resp.ExpiresAt.Before(before.Add(wantLifetime-time.Second)) || resp.ExpiresAt.After(after.Add(wantLifetime+time.Second)) {
		t.Errorf("ExpiresAt = %v, want close to now+%v", resp.ExpiresAt, wantLifetime)
	}
}

// --- Rotation grace window: an already-minted token still verifies
// while its own signing key is inside the overlap window, and the key
// drops out of JWKS discovery once that window has elapsed ---

// setSigningKeyRetiredAt directly rewrites kid's own retired_at column --
// simulating the passage of time past the overlap window without
// actually sleeping the test for platform.Timeouts.
// CloudIdentitySigningKeyOverlapWindow's own real duration (15 minutes by
// default). This is the ONE place in this file that reaches around the
// REST API, deliberately: proving the ROTATION endpoint's own
// retiredAt/publishableUntil arithmetic is TestRotateCloudIdentitySigningKey_
// SecondRotationRetiresFirst's own job (cloudidentitykeys_integration_
// test.go); this test's own job is proving the JWKS endpoint's own
// publish-window FILTER, which is exactly platform.Timeouts.
// CloudIdentitySigningKeyOverlapWindow applied to whatever retired_at
// value is actually stored -- a real rotation's own retired_at (recent)
// and a synthetically-aged one (this helper's own aged value) exercise
// the IDENTICAL filter, just at different ages.
func setSigningKeyRetiredAt(ctx context.Context, t *testing.T, r testRig, kid string, retiredAt time.Time) {
	t.Helper()
	if _, err := r.pool.Exec(ctx, `UPDATE oidc_signing_keys SET retired_at = $2 WHERE kid = $1`, kid, retiredAt); err != nil {
		t.Fatalf("set retired_at for kid %q: %v", kid, err)
	}
}

func TestOIDCJWKS_RotationGraceWindow(t *testing.T) {
	rig := newTestRig(t)

	first := rotateSigningKeyViaAPI(t, rig)
	second := rotateSigningKeyViaAPI(t, rig)
	if second.RetiredKid == nil || *second.RetiredKid != first.ActiveKid {
		t.Fatalf("second rotation did not retire the first key: %+v", second)
	}

	// Immediately after rotation, the just-retired key is still well
	// inside its own overlap window (real retiredAt is "now", the window
	// is 15 minutes) -- still published.
	keys := fetchJWKS(t, rig)
	if !jwkSetContainsKid(keys, first.ActiveKid) {
		t.Errorf("first key %q missing from JWKS immediately after retirement (still inside overlap window)", first.ActiveKid)
	}
	if !jwkSetContainsKid(keys, second.ActiveKid) {
		t.Errorf("second (currently active) key %q missing from JWKS", second.ActiveKid)
	}

	// Age the retired key's own retired_at past the overlap window --
	// simulating what real wall-clock time passing would eventually
	// produce.
	overlap := platform.DefaultTimeouts().CloudIdentitySigningKeyOverlapWindow
	setSigningKeyRetiredAt(context.Background(), t, rig, first.ActiveKid, time.Now().Add(-overlap-time.Hour))

	keys = fetchJWKS(t, rig)
	if jwkSetContainsKid(keys, first.ActiveKid) {
		t.Errorf("first key %q still present in JWKS after its overlap window elapsed -- rotation grace window is not being enforced", first.ActiveKid)
	}
	if !jwkSetContainsKid(keys, second.ActiveKid) {
		t.Errorf("second (currently active) key %q missing from JWKS", second.ActiveKid)
	}
}

func jwkSetContainsKid(keys []oidcsigning.JWK, kid string) bool {
	for _, k := range keys {
		if k.Kid == kid {
			return true
		}
	}
	return false
}
