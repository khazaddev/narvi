//go:build integration

// Integration tests for §27.3's own ("cloud identity: OIDC issuer,
// bindings, minting", §27.3) CP-side management CRUD surface
// (cloudidentitybindings.go), against a real Postgres instance -- sharing
// this package's own testRig (httpapi_integration_test.go).
package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// createEnvironment inserts a bare environments row and returns it --
// this codebase has no standalone Environment CRUD endpoint (migrations/
// 000021_environments.up.sql's own doc comment), so tests that need a
// real environment id to bind against create one directly via the store,
// mirroring httpapi.CreateSession's own inline-creation precedent.
func createEnvironment(ctx context.Context, t *testing.T, r testRig) sqlcgen.Environment {
	t.Helper()
	env, err := r.environments.Create(ctx, sqlcgen.CreateEnvironmentParams{})
	if err != nil {
		t.Fatalf("create environment: %v", err)
	}
	return env
}

// --- RBAC ---

func TestCreateEnvironmentCloudIdentityBinding_MemberDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	env := createEnvironment(ctx, t, rig)
	_, token := rig.createAuthenticatedUser(ctx, t) // default role: member.

	status := rig.doJSON(t, http.MethodPost, "/api/environments/"+env.ID.String()+"/cloud-identity-bindings",
		[]byte(`{"kind":"aws","audience":"sts.amazonaws.com"}`), nil, token)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

func TestCreateEnvironmentCloudIdentityBinding_MaintainerAllowed(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	env := createEnvironment(ctx, t, rig)
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	var got restdtos.CloudIdentityBinding
	status := rig.doJSON(t, http.MethodPost, "/api/environments/"+env.ID.String()+"/cloud-identity-bindings",
		[]byte(`{"kind":"aws","audience":"sts.amazonaws.com","params":{"roleArn":"arn:aws:iam::123456789012:role/narvi"}}`), &got, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}
	if got.Scope != restdtos.CloudIdentityBindingScopeEnvironment {
		t.Errorf("Scope = %q, want environment", got.Scope)
	}
	if got.ScopeTarget == nil || *got.ScopeTarget != env.ID.String() {
		t.Errorf("ScopeTarget = %v, want %q", got.ScopeTarget, env.ID.String())
	}
	if got.Kind != restdtos.CloudIdentityBindingKindAws {
		t.Errorf("Kind = %q, want aws", got.Kind)
	}
}

// TestCreateGlobalCloudIdentityBinding_MaintainerAllowed proves the
// global route group is ALSO maintainer+ -- unlike provider_credentials'
// own admin-only global scope, both cloud-identity-bindings route groups
// share the SAME action (authz.ActionManageCloudIdentityBindings, row 4)
// -- see that action's own doc comment for why.
func TestCreateGlobalCloudIdentityBinding_MaintainerAllowed(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	var got restdtos.CloudIdentityBinding
	status := rig.doJSON(t, http.MethodPost, "/api/cloud-identity-bindings",
		[]byte(`{"kind":"generic","audience":"vault.example.test"}`), &got, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}
	if got.Scope != restdtos.CloudIdentityBindingScopeGlobal {
		t.Errorf("Scope = %q, want global", got.Scope)
	}
	if got.ScopeTarget != nil {
		t.Errorf("ScopeTarget = %v, want nil", got.ScopeTarget)
	}
}

// --- Capability off: binding CRUD refuses, fail-closed (§27.3) ---

// TestCreateEnvironmentCloudIdentityBinding_IssuerUnset_FailsClosed and
// TestCreateGlobalCloudIdentityBinding_IssuerUnset_FailsClosed pin §27.3's
// own explicit requirement, verbatim: "the whole capability is off (and
// binding CRUD refuses, fail-closed) when unset" -- repeated at the Step
// 73 row: "capability off and binding CRUD refusing, fail-closed, when
// unset". An adversarial review found this entirely UNIMPLEMENTED:
// cloudidentitybindings.go's own 4 shared handler cores (create/list/
// update/delete) took no issuerURL parameter at all, and cmd/
// control-plane/main.go mounted both binding route groups behind
// auth.Middleware alone -- binding CRUD stayed fail-OPEN (writable) the
// entire time the capability was off, contradicting the spec and
// platform.Config.CloudIdentityIssuerURL's own doc comment (which
// incorrectly claimed every cloud-identity surface already checked this
// field). This file previously had NO 503 assertion at all, and this
// package's own testRig hard-codes a non-empty issuer
// (httpapi_integration_test.go's own newTestRig) -- these two tests are
// the missing coverage.
//
// Both route groups now share httpapi.RequireCloudIdentityCapability,
// mounted once per group (cmd/control-plane/main.go's own r.Use(...),
// mirrored in this package's own testRig router construction,
// httpapi_integration_test.go) -- TWO tests, not one, because each group
// carries its OWN separate r.Use(...) call that could independently
// regress without the other noticing.
//
// Mutation test (run manually during verification, reverted immediately
// after, byte-identical): removing EITHER group's own
// r.Use(httpapi.RequireCloudIdentityCapability(...)) call (in this
// package's own httpapi_integration_test.go AND in cmd/control-plane/
// main.go) must make the CORRESPONDING one of these two tests fail with
// 201 instead of 503.
func TestCreateEnvironmentCloudIdentityBinding_IssuerUnset_FailsClosed(t *testing.T) {
	rig := newTestRig(t, func(r *testRig) { r.cloudIdentityIssuerURL = "" })
	ctx := context.Background()
	env := createEnvironment(ctx, t, rig)
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	status := rig.doJSON(t, http.MethodPost, "/api/environments/"+env.ID.String()+"/cloud-identity-bindings",
		[]byte(`{"kind":"aws","audience":"sts.amazonaws.com"}`), nil, token)
	if status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d (capability off must refuse binding CRUD, fail-closed, §27.3)", status, http.StatusServiceUnavailable)
	}
}

func TestCreateGlobalCloudIdentityBinding_IssuerUnset_FailsClosed(t *testing.T) {
	rig := newTestRig(t, func(r *testRig) { r.cloudIdentityIssuerURL = "" })
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	status := rig.doJSON(t, http.MethodPost, "/api/cloud-identity-bindings",
		[]byte(`{"kind":"generic","audience":"vault.example.test"}`), nil, token)
	if status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d (capability off must refuse binding CRUD, fail-closed, §27.3)", status, http.StatusServiceUnavailable)
	}
}

// --- Gap 3: azure + global refused ---

// TestCreateGlobalCloudIdentityBinding_AzureRefused mutation-tests this
// Step's own gap-3 resolution at the HTTP layer: removing the ValidateBinding
// call inside createCloudIdentityBinding (or its own azure+global branch)
// must make this test fail.
func TestCreateGlobalCloudIdentityBinding_AzureRefused(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	status := rig.doJSON(t, http.MethodPost, "/api/cloud-identity-bindings",
		[]byte(`{"kind":"azure","audience":"api://AzureADTokenExchange"}`), nil, token)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (azure+global must be refused)", status, http.StatusBadRequest)
	}
}

// TestCreateEnvironmentCloudIdentityBinding_AzureAllowed proves azure IS
// allowed at environment scope -- only the global combination is refused.
func TestCreateEnvironmentCloudIdentityBinding_AzureAllowed(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	env := createEnvironment(ctx, t, rig)
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	status := rig.doJSON(t, http.MethodPost, "/api/environments/"+env.ID.String()+"/cloud-identity-bindings",
		[]byte(`{"kind":"azure","audience":"api://AzureADTokenExchange","params":{"clientId":"11111111-1111-1111-1111-111111111111","tenantId":"22222222-2222-2222-2222-222222222222"}}`), nil, token)
	if status != http.StatusCreated {
		t.Errorf("status = %d, want %d (azure at environment scope must be allowed)", status, http.StatusCreated)
	}
}

// --- Gap 4: the response surfaces the exact sub string ---

func TestCreateEnvironmentCloudIdentityBinding_SubSurfaced(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	env := createEnvironment(ctx, t, rig)
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	var got restdtos.CloudIdentityBinding
	status := rig.doJSON(t, http.MethodPost, "/api/environments/"+env.ID.String()+"/cloud-identity-bindings",
		[]byte(`{"kind":"gcp","audience":"//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/narvi/providers/narvi"}`), &got, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}
	wantSub := "narvi:environment:" + env.ID.String()
	if got.Sub == nil || *got.Sub != wantSub {
		t.Errorf("Sub = %v, want %q", got.Sub, wantSub)
	}
}

// TestCreateGlobalCloudIdentityBinding_SubIsNil proves a global-scoped
// binding's own sub is null -- there is no single string to surface
// (this Step's own gap-3 discussion, internal/domain/cloudidentity's own
// doc comment).
func TestCreateGlobalCloudIdentityBinding_SubIsNil(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	var got restdtos.CloudIdentityBinding
	status := rig.doJSON(t, http.MethodPost, "/api/cloud-identity-bindings",
		[]byte(`{"kind":"gcp","audience":"//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/narvi/providers/narvi"}`), &got, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}
	if got.Sub != nil {
		t.Errorf("Sub = %q, want nil for a global-scoped binding", *got.Sub)
	}
}

// --- CRUD lifecycle ---

func TestCloudIdentityBindingLifecycle_CreateListUpdateDelete(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	env := createEnvironment(ctx, t, rig)
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	var created restdtos.CloudIdentityBinding
	status := rig.doJSON(t, http.MethodPost, "/api/environments/"+env.ID.String()+"/cloud-identity-bindings",
		[]byte(`{"kind":"aws","audience":"sts.amazonaws.com"}`), &created, token)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d, want %d", status, http.StatusCreated)
	}

	var list restdtos.ListCloudIdentityBindingsResponse
	status = rig.doJSON(t, http.MethodGet, "/api/environments/"+env.ID.String()+"/cloud-identity-bindings", nil, &list, token)
	if status != http.StatusOK {
		t.Fatalf("list status = %d, want %d", status, http.StatusOK)
	}
	if len(list.CloudIdentityBindings) != 1 || list.CloudIdentityBindings[0].Id != created.Id {
		t.Fatalf("list = %+v, want exactly the created binding", list.CloudIdentityBindings)
	}

	var updated restdtos.CloudIdentityBinding
	status = rig.doJSON(t, http.MethodPut, "/api/environments/"+env.ID.String()+"/cloud-identity-bindings/"+created.Id,
		[]byte(`{"audience":"sts.amazonaws.com","params":{"roleArn":"arn:aws:iam::999999999999:role/rotated"}}`), &updated, token)
	if status != http.StatusOK {
		t.Fatalf("update status = %d, want %d", status, http.StatusOK)
	}
	if string(updated.Params) == string(created.Params) {
		t.Errorf("params did not change across update: %s", updated.Params)
	}

	status = rig.doJSON(t, http.MethodDelete, "/api/environments/"+env.ID.String()+"/cloud-identity-bindings/"+created.Id, nil, nil, token)
	if status != http.StatusNoContent {
		t.Fatalf("delete status = %d, want %d", status, http.StatusNoContent)
	}

	status = rig.doJSON(t, http.MethodGet, "/api/environments/"+env.ID.String()+"/cloud-identity-bindings", nil, &list, token)
	if status != http.StatusOK {
		t.Fatalf("list-after-delete status = %d, want %d", status, http.StatusOK)
	}
	if len(list.CloudIdentityBindings) != 0 {
		t.Errorf("list after delete = %+v, want empty", list.CloudIdentityBindings)
	}
}

// TestCreateCloudIdentityBinding_DuplicateKindConflicts proves the
// "at most one per (scope target, kind) in v1" rule (§27.3).
func TestCreateCloudIdentityBinding_DuplicateKindConflicts(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	env := createEnvironment(ctx, t, rig)
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	body := []byte(`{"kind":"aws","audience":"sts.amazonaws.com"}`)
	status := rig.doJSON(t, http.MethodPost, "/api/environments/"+env.ID.String()+"/cloud-identity-bindings", body, nil, token)
	if status != http.StatusCreated {
		t.Fatalf("first create status = %d, want %d", status, http.StatusCreated)
	}
	status = rig.doJSON(t, http.MethodPost, "/api/environments/"+env.ID.String()+"/cloud-identity-bindings", body, nil, token)
	if status != http.StatusConflict {
		t.Errorf("second create status = %d, want %d", status, http.StatusConflict)
	}
}

// TestUpdateCloudIdentityBinding_CrossScopeRowConfusion404s proves the
// same IDOR-shaped closure providercredentials.go's own
// getProviderCredentialInScope establishes: a binding created at GLOBAL
// scope cannot be updated through the ENVIRONMENT-scoped route, even
// though the caller passes a real, existing id.
func TestUpdateCloudIdentityBinding_CrossScopeRowConfusion404s(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	env := createEnvironment(ctx, t, rig)
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	var globalBinding restdtos.CloudIdentityBinding
	status := rig.doJSON(t, http.MethodPost, "/api/cloud-identity-bindings",
		[]byte(`{"kind":"aws","audience":"sts.amazonaws.com"}`), &globalBinding, token)
	if status != http.StatusCreated {
		t.Fatalf("create global binding status = %d, want %d", status, http.StatusCreated)
	}

	status = rig.doJSON(t, http.MethodPut, "/api/environments/"+env.ID.String()+"/cloud-identity-bindings/"+globalBinding.Id,
		[]byte(`{"audience":"sts.amazonaws.com"}`), nil, token)
	if status != http.StatusNotFound {
		t.Errorf("cross-scope update status = %d, want %d", status, http.StatusNotFound)
	}
}

// --- Audit log ---

// TestCreateCloudIdentityBinding_RecordsAuditLog proves binding CRUD
// writes audit_log (§27.3: "audit_log records binding CRUD, not each
// 5-minute refresh").
func TestCreateCloudIdentityBinding_RecordsAuditLog(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	env := createEnvironment(ctx, t, rig)
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	status := rig.doJSON(t, http.MethodPost, "/api/environments/"+env.ID.String()+"/cloud-identity-bindings",
		[]byte(`{"kind":"aws","audience":"sts.amazonaws.com"}`), nil, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}

	entries, err := rig.auditLog.List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("list audit log: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.Action == "cloud_identity_binding.created" && e.ResourceType == "cloud_identity_binding" {
			found = true
		}
	}
	if !found {
		t.Errorf("no cloud_identity_binding.created audit_log entry found among %d entries", len(entries))
	}
}
