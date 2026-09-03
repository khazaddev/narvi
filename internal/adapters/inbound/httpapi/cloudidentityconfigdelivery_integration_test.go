//go:build integration

// Integration tests for §27.4's own ("cloud identity: sandbox-side
// consumption + kubeconfig injection", §27.3/§27.4) sandbox-facing
// discovery endpoint (cloudidentityconfigdelivery.go), against a real
// Postgres instance -- sharing this package's own testRig
// (httpapi_integration_test.go), createEnvironment
// (cloudidentitybindings_integration_test.go), and
// createSessionWithReposAndEnvironment/providerCredsRepos
// (providercredentialsdelivery_integration_test.go).
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

type cloudIdentityConfigBindingWire struct {
	Kind     string          `json:"kind"`
	Audience string          `json:"audience"`
	Params   json.RawMessage `json:"params"`
}

type cloudIdentityConfigClusterWire struct {
	Name      string          `json:"name"`
	ServerUrl *string         `json:"serverUrl"`
	CaBundle  *string         `json:"caBundle"`
	AuthKind  string          `json:"authKind"`
	Params    json.RawMessage `json:"params"`
}

type cloudIdentityConfigWire struct {
	Bindings       []cloudIdentityConfigBindingWire `json:"bindings"`
	ClusterBinding *cloudIdentityConfigClusterWire  `json:"clusterBinding"`
}

func postCloudIdentityConfig(t *testing.T, r testRig, sessionID, bearer, gen string) (int, cloudIdentityConfigWire) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, r.server.URL+"/sessions/"+sessionID+"/cloud-identity-config", strings.NewReader(""))
	if err != nil {
		t.Fatalf("build request: %v", err)
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

	var got cloudIdentityConfigWire
	_ = json.NewDecoder(resp.Body).Decode(&got)
	return resp.StatusCode, got
}

// --- Auth / dead-sandbox / gen-fencing (mirrors cloudidentitytoken_integration_test.go) ---

func TestCloudIdentityConfigDelivery_MissingBearer(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	status, _ := postCloudIdentityConfig(t, rig, session.ID.String(), "", "1")
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

func TestCloudIdentityConfigDelivery_UnknownSession(t *testing.T) {
	rig := newTestRig(t)
	status, _ := postCloudIdentityConfig(t, rig, "11111111-1111-1111-1111-111111111111", "any-token", "1")
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

func TestCloudIdentityConfigDelivery_DeadSandboxStatus(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")
	moveSandboxStatus(ctx, t, rig, session.ID, sqlcgen.SandboxStatusStopped)

	status, _ := postCloudIdentityConfig(t, rig, session.ID.String(), "sandbox-bearer-token", "1")
	if status != http.StatusGone {
		t.Errorf("status = %d, want %d", status, http.StatusGone)
	}
}

func TestCloudIdentityConfigDelivery_GenMismatch_Rejected(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token") // gen 1

	status, _ := postCloudIdentityConfig(t, rig, session.ID.String(), "sandbox-bearer-token", "999")
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

func TestCloudIdentityConfigDelivery_InvalidBearer(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	status, _ := postCloudIdentityConfig(t, rig, session.ID.String(), "totally-wrong-token", "1")
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

// --- No environment: empty bindings, no cluster ---

func TestCloudIdentityConfigDelivery_NoEnvironment_EmptyResponse(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	status, got := postCloudIdentityConfig(t, rig, session.ID.String(), "sandbox-bearer-token", "1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if len(got.Bindings) != 0 {
		t.Errorf("Bindings = %v, want empty", got.Bindings)
	}
	if got.ClusterBinding != nil {
		t.Errorf("ClusterBinding = %v, want nil", got.ClusterBinding)
	}
}

// --- Resolves bindings: environment shadows global for the SAME kind ---

func TestCloudIdentityConfigDelivery_EnvironmentBindingShadowsGlobal(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, true)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	createGlobalBindingViaAPI(t, rig, "aws", "global-audience")
	createEnvironmentBindingViaAPI(t, rig, session.EnvironmentID.String(), "aws", "env-audience")
	createGlobalBindingViaAPI(t, rig, "gcp", "global-gcp-audience")

	status, got := postCloudIdentityConfig(t, rig, session.ID.String(), "sandbox-bearer-token", "1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	byKind := map[string]string{}
	for _, b := range got.Bindings {
		byKind[b.Kind] = b.Audience
	}
	if byKind["aws"] != "env-audience" {
		t.Errorf("aws audience = %q, want %q (environment-scoped must shadow global)", byKind["aws"], "env-audience")
	}
	if byKind["gcp"] != "global-gcp-audience" {
		t.Errorf("gcp audience = %q, want %q (global fallback, no environment override)", byKind["gcp"], "global-gcp-audience")
	}
}

// --- Cluster binding included alongside bindings ---

func TestCloudIdentityConfigDelivery_IncludesClusterBinding(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, true)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	_, maintToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)
	putStatus := rig.doJSON(t, http.MethodPut, "/api/environments/"+session.EnvironmentID.String()+"/cluster-binding",
		[]byte(`{"name":"prod","authKind":"static","params":{"secretName":"KUBE_STATIC_CONFIG"}}`), nil, maintToken)
	if putStatus != http.StatusOK {
		t.Fatalf("put cluster binding: status = %d, want %d", putStatus, http.StatusOK)
	}

	status, got := postCloudIdentityConfig(t, rig, session.ID.String(), "sandbox-bearer-token", "1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got.ClusterBinding == nil {
		t.Fatal("ClusterBinding = nil, want a configured cluster")
	}
	if got.ClusterBinding.Name != "prod" {
		t.Errorf("ClusterBinding.Name = %q, want %q", got.ClusterBinding.Name, "prod")
	}
	if got.ClusterBinding.AuthKind != "static" {
		t.Errorf("ClusterBinding.AuthKind = %q, want %q", got.ClusterBinding.AuthKind, "static")
	}
}

// --- Not gated by RequireCloudIdentityCapability ---

// TestCloudIdentityConfigDelivery_WorksEvenWhenCapabilityOff proves this
// endpoint is deliberately NOT behind RequireCloudIdentityCapability (see
// this file's own doc comment in cloudidentityconfigdelivery.go): a
// static-rung cluster binding needs no OIDC issuer at all, so the
// discovery read itself must keep working even when
// cfg.CloudIdentityIssuerURL is unset -- only the actual token MINT (a
// separate, already-gated endpoint) enforces that fail-closed rule.
func TestCloudIdentityConfigDelivery_WorksEvenWhenCapabilityOff(t *testing.T) {
	rig := newTestRig(t, func(r *testRig) { r.cloudIdentityIssuerURL = "" })
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, true)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	_, maintToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)
	putStatus := rig.doJSON(t, http.MethodPut, "/api/environments/"+session.EnvironmentID.String()+"/cluster-binding",
		[]byte(`{"name":"prod","authKind":"static","params":{"secretName":"KUBE_STATIC_CONFIG"}}`), nil, maintToken)
	if putStatus != http.StatusOK {
		t.Fatalf("put cluster binding: status = %d, want %d", putStatus, http.StatusOK)
	}

	status, got := postCloudIdentityConfig(t, rig, session.ID.String(), "sandbox-bearer-token", "1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (capability-off must not gate this endpoint)", status, http.StatusOK)
	}
	if got.ClusterBinding == nil {
		t.Error("ClusterBinding = nil, want the static-rung cluster to still be delivered")
	}
}
