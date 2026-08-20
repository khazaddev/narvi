//go:build integration

// Integration tests for §27.4's own ("cloud identity: sandbox-side
// consumption + kubeconfig injection", §27.4) CP-side management CRUD
// surface (clusterbindings.go), against a real Postgres instance --
// sharing this package's own testRig (httpapi_integration_test.go) and
// createEnvironment (cloudidentitybindings_integration_test.go).
package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// --- RBAC ---

func TestPutEnvironmentClusterBinding_MemberDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	env := createEnvironment(ctx, t, rig)
	_, token := rig.createAuthenticatedUser(ctx, t) // default role: member.

	status := rig.doJSON(t, http.MethodPut, "/api/environments/"+env.ID.String()+"/cluster-binding",
		[]byte(`{"name":"prod","authKind":"static","params":{"secretName":"KUBE_STATIC_CONFIG"}}`), nil, token)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

func TestPutEnvironmentClusterBinding_MaintainerAllowed(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	env := createEnvironment(ctx, t, rig)
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	var got restdtos.ClusterBinding
	status := rig.doJSON(t, http.MethodPut, "/api/environments/"+env.ID.String()+"/cluster-binding",
		[]byte(`{"name":"prod","authKind":"static","params":{"secretName":"KUBE_STATIC_CONFIG"}}`), &got, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got.EnvironmentId != env.ID.String() {
		t.Errorf("EnvironmentId = %q, want %q", got.EnvironmentId, env.ID.String())
	}
	if got.Name != "prod" {
		t.Errorf("Name = %q, want %q", got.Name, "prod")
	}
	if got.AuthKind != restdtos.ClusterBindingAuthKindStatic {
		t.Errorf("AuthKind = %q, want static", got.AuthKind)
	}
}

// --- GET: not-found vs found ---

func TestGetEnvironmentClusterBinding_NotConfigured404(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	env := createEnvironment(ctx, t, rig)
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	status := rig.doJSON(t, http.MethodGet, "/api/environments/"+env.ID.String()+"/cluster-binding", nil, nil, token)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

func TestGetEnvironmentClusterBinding_RoundTrip(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	env := createEnvironment(ctx, t, rig)
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	status := rig.doJSON(t, http.MethodPut, "/api/environments/"+env.ID.String()+"/cluster-binding",
		[]byte(`{"name":"prod-eks","authKind":"cloud","serverUrl":"https://eks.example","caBundle":"ca-bundle-pem","params":{"cloud":"aws"}}`), nil, token)
	if status != http.StatusOK {
		t.Fatalf("put status = %d, want %d", status, http.StatusOK)
	}

	var got restdtos.ClusterBinding
	status = rig.doJSON(t, http.MethodGet, "/api/environments/"+env.ID.String()+"/cluster-binding", nil, &got, token)
	if status != http.StatusOK {
		t.Fatalf("get status = %d, want %d", status, http.StatusOK)
	}
	if got.Name != "prod-eks" {
		t.Errorf("Name = %q, want %q", got.Name, "prod-eks")
	}
	if got.ServerUrl == nil || *got.ServerUrl != "https://eks.example" {
		t.Errorf("ServerUrl = %v, want %q", got.ServerUrl, "https://eks.example")
	}
	if got.AuthKind != restdtos.ClusterBindingAuthKindCloud {
		t.Errorf("AuthKind = %q, want cloud", got.AuthKind)
	}
}

// --- PUT is upsert: a second PUT replaces, never a second row ---

func TestPutEnvironmentClusterBinding_SecondPutReplaces(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	env := createEnvironment(ctx, t, rig)
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	status := rig.doJSON(t, http.MethodPut, "/api/environments/"+env.ID.String()+"/cluster-binding",
		[]byte(`{"name":"prod-v1","authKind":"static","params":{"secretName":"S1"}}`), nil, token)
	if status != http.StatusOK {
		t.Fatalf("first put status = %d, want %d", status, http.StatusOK)
	}

	var got restdtos.ClusterBinding
	status = rig.doJSON(t, http.MethodPut, "/api/environments/"+env.ID.String()+"/cluster-binding",
		[]byte(`{"name":"prod-v2","authKind":"static","params":{"secretName":"S2"}}`), &got, token)
	if status != http.StatusOK {
		t.Fatalf("second put status = %d, want %d", status, http.StatusOK)
	}
	if got.Name != "prod-v2" {
		t.Errorf("Name = %q, want %q (the second PUT must replace, not add a second row)", got.Name, "prod-v2")
	}
}

// --- Validation ---

func TestPutEnvironmentClusterBinding_CloudMissingServerURL_400(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	env := createEnvironment(ctx, t, rig)
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	status := rig.doJSON(t, http.MethodPut, "/api/environments/"+env.ID.String()+"/cluster-binding",
		[]byte(`{"name":"prod","authKind":"cloud","caBundle":"ca","params":{"cloud":"aws"}}`), nil, token)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (cloud rung requires serverUrl)", status, http.StatusBadRequest)
	}
}

func TestPutEnvironmentClusterBinding_InvalidParams_400(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	env := createEnvironment(ctx, t, rig)
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	status := rig.doJSON(t, http.MethodPut, "/api/environments/"+env.ID.String()+"/cluster-binding",
		[]byte(`{"name":"prod","authKind":"static","params":{}}`), nil, token) // missing secretName
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (static rung requires params.secretName)", status, http.StatusBadRequest)
	}
}

// --- DELETE ---

func TestDeleteEnvironmentClusterBinding_RemovesRow(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	env := createEnvironment(ctx, t, rig)
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	status := rig.doJSON(t, http.MethodPut, "/api/environments/"+env.ID.String()+"/cluster-binding",
		[]byte(`{"name":"prod","authKind":"static","params":{"secretName":"S"}}`), nil, token)
	if status != http.StatusOK {
		t.Fatalf("put status = %d, want %d", status, http.StatusOK)
	}

	status = rig.doJSON(t, http.MethodDelete, "/api/environments/"+env.ID.String()+"/cluster-binding", nil, nil, token)
	if status != http.StatusNoContent {
		t.Fatalf("delete status = %d, want %d", status, http.StatusNoContent)
	}

	status = rig.doJSON(t, http.MethodGet, "/api/environments/"+env.ID.String()+"/cluster-binding", nil, nil, token)
	if status != http.StatusNotFound {
		t.Errorf("get after delete status = %d, want %d", status, http.StatusNotFound)
	}
}

func TestDeleteEnvironmentClusterBinding_NotConfigured404(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	env := createEnvironment(ctx, t, rig)
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	status := rig.doJSON(t, http.MethodDelete, "/api/environments/"+env.ID.String()+"/cluster-binding", nil, nil, token)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}
