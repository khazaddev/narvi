//go:build integration

// Integration tests for Step 72's own ("sandbox secrets & opencode
// config", §27.1) CP-side management CRUD surface (sandboxsecrets.go),
// against a real Postgres instance -- sharing this package's own testRig
// (httpapi_integration_test.go) and mirroring
// providercredentials_integration_test.go's own test shapes.
package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// --- RBAC ---

func TestCreateRepoSandboxSecret_MemberDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t) // default role: member.

	status := rig.doJSON(t, http.MethodPost, "/api/repos/acme/widgets/sandbox-secrets",
		[]byte(`{"name":"MY_SECRET","value":"should-never-be-stored"}`), nil, token)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

func TestCreateRepoSandboxSecret_MaintainerAllowed(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)
	rig.markRepoKnown(ctx, t, "acme/widgets")

	var got restdtos.SandboxSecret
	status := rig.doJSON(t, http.MethodPost, "/api/repos/acme/widgets/sandbox-secrets",
		[]byte(`{"name":"MY_SECRET","value":"real-value"}`), &got, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}
	if got.Scope != restdtos.SandboxSecretScopeRepo {
		t.Errorf("Scope = %q, want %q", got.Scope, restdtos.SandboxSecretScopeRepo)
	}
	if got.ScopeTarget == nil || *got.ScopeTarget != "acme/widgets" {
		t.Errorf("ScopeTarget = %v, want %q", got.ScopeTarget, "acme/widgets")
	}
	if got.Name != "MY_SECRET" {
		t.Errorf("Name = %q, want %q", got.Name, "MY_SECRET")
	}
}

func TestCreateEnvironmentSandboxSecret_MemberDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	status := rig.doJSON(t, http.MethodPost, "/api/environments/11111111-1111-1111-1111-111111111111/sandbox-secrets",
		[]byte(`{"name":"MY_SECRET","value":"should-never-be-stored"}`), nil, token)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

func TestCreateGlobalSandboxSecret_MaintainerDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	status := rig.doJSON(t, http.MethodPost, "/api/sandbox-secrets",
		[]byte(`{"name":"MY_SECRET","value":"should-never-be-stored"}`), nil, token)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d (admin only, no maintainer carve-out)", status, http.StatusForbidden)
	}
}

func TestCreateGlobalSandboxSecret_AdminAllowed(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	var got restdtos.SandboxSecret
	status := rig.doJSON(t, http.MethodPost, "/api/sandbox-secrets",
		[]byte(`{"name":"MY_SECRET","value":"real-value"}`), &got, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}
	if got.Scope != restdtos.SandboxSecretScopeGlobal {
		t.Errorf("Scope = %q, want %q", got.Scope, restdtos.SandboxSecretScopeGlobal)
	}
	if got.ScopeTarget != nil {
		t.Errorf("ScopeTarget = %v, want nil (global)", got.ScopeTarget)
	}
}

// --- Write-only value / masking ---

func TestCreateSandboxSecret_ValueNeverReturned(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	const secret = "super-secret-value-must-never-leak"
	var got restdtos.SandboxSecret
	status := rig.doJSON(t, http.MethodPost, "/api/sandbox-secrets",
		[]byte(fmt.Sprintf(`{"name":"MY_SECRET","value":%q}`, secret)), &got, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}
	if got.MaskedValue == secret {
		t.Errorf("MaskedValue = %q, must never equal the real value", got.MaskedValue)
	}
}

// --- Name validation, fail-closed (§27.1) ---

func TestCreateSandboxSecret_NarviReservedName_Rejected(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	status := rig.doJSON(t, http.MethodPost, "/api/sandbox-secrets",
		[]byte(`{"name":"NARVI_BOOT_MODE","value":"anything"}`), nil, token)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (NARVI_* namespace reserved)", status, http.StatusBadRequest)
	}
}

// TestCreateSandboxSecret_ProviderCredentialReservedName_Rejected proves
// the "one owning mechanism per env-var name" collision rule end to end
// through the REST layer, not just internal/domain/sandboxsecret's own
// unit tests.
func TestCreateSandboxSecret_ProviderCredentialReservedName_Rejected(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	status := rig.doJSON(t, http.MethodPost, "/api/sandbox-secrets",
		[]byte(`{"name":"ANTHROPIC_API_KEY","value":"anything"}`), nil, token)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (already owned by provider credential injection)", status, http.StatusBadRequest)
	}
}

func TestCreateSandboxSecret_LowercaseName_Rejected(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	status := rig.doJSON(t, http.MethodPost, "/api/sandbox-secrets",
		[]byte(`{"name":"my_secret","value":"anything"}`), nil, token)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (not POSIX env-var shaped)", status, http.StatusBadRequest)
	}
}

func TestCreateSandboxSecret_ValidName_Accepted(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	status := rig.doJSON(t, http.MethodPost, "/api/sandbox-secrets",
		[]byte(`{"name":"MY_CUSTOM_SECRET","value":"anything"}`), nil, token)
	if status != http.StatusCreated {
		t.Errorf("status = %d, want %d", status, http.StatusCreated)
	}
}

// --- Duplicate rejection ---

func TestCreateSandboxSecret_DuplicateNameAtSameScope_Conflict(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	status := rig.doJSON(t, http.MethodPost, "/api/sandbox-secrets",
		[]byte(`{"name":"DUP_SECRET","value":"first"}`), nil, token)
	if status != http.StatusCreated {
		t.Fatalf("first create status = %d, want %d", status, http.StatusCreated)
	}
	status = rig.doJSON(t, http.MethodPost, "/api/sandbox-secrets",
		[]byte(`{"name":"DUP_SECRET","value":"second"}`), nil, token)
	if status != http.StatusConflict {
		t.Errorf("second create status = %d, want %d", status, http.StatusConflict)
	}
}

// --- Cross-scope row confusion (IDOR-shaped risk) ---

// TestUpdateRepoSandboxSecretValue_GlobalRowID_NotFound proves a
// maintainer holding ActionManageRepoSecrets (but NOT
// ActionManageGlobalSecrets) cannot rotate a GLOBAL secret's value by
// guessing/learning its id and hitting the repo-scoped PUT route with it
// -- getSandboxSecretInScope must render a plain 404, never leak that the
// id exists at a different scope.
func TestUpdateRepoSandboxSecretValue_GlobalRowID_NotFound(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, adminToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	var globalSecret restdtos.SandboxSecret
	status := rig.doJSON(t, http.MethodPost, "/api/sandbox-secrets",
		[]byte(`{"name":"GLOBAL_ONLY","value":"global-value"}`), &globalSecret, adminToken)
	if status != http.StatusCreated {
		t.Fatalf("create global secret status = %d, want %d", status, http.StatusCreated)
	}

	_, maintainerToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)
	rig.markRepoKnown(ctx, t, "acme/widgets")

	status = rig.doJSON(t, http.MethodPut, "/api/repos/acme/widgets/sandbox-secrets/"+globalSecret.Id,
		[]byte(`{"value":"attacker-supplied-value"}`), nil, maintainerToken)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d (cross-scope row confusion must render 404, never succeed)", status, http.StatusNotFound)
	}
}

// --- List ---

func TestListGlobalSandboxSecrets_ReturnsCreatedRow(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	status := rig.doJSON(t, http.MethodPost, "/api/sandbox-secrets",
		[]byte(`{"name":"LISTED_SECRET","value":"v"}`), nil, token)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d, want %d", status, http.StatusCreated)
	}

	var got restdtos.ListSandboxSecretsResponse
	status = rig.doJSON(t, http.MethodGet, "/api/sandbox-secrets", nil, &got, token)
	if status != http.StatusOK {
		t.Fatalf("list status = %d, want %d", status, http.StatusOK)
	}
	found := false
	for _, s := range got.SandboxSecrets {
		if s.Name == "LISTED_SECRET" {
			found = true
		}
	}
	if !found {
		t.Errorf("ListSandboxSecretsResponse = %+v, want it to contain LISTED_SECRET", got.SandboxSecrets)
	}
}

// --- Delete ---

func TestDeleteGlobalSandboxSecret_RemovesRow(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	var created restdtos.SandboxSecret
	status := rig.doJSON(t, http.MethodPost, "/api/sandbox-secrets",
		[]byte(`{"name":"TO_DELETE","value":"v"}`), &created, token)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d, want %d", status, http.StatusCreated)
	}

	status = rig.doJSON(t, http.MethodDelete, "/api/sandbox-secrets/"+created.Id, nil, nil, token)
	if status != http.StatusNoContent {
		t.Fatalf("delete status = %d, want %d", status, http.StatusNoContent)
	}

	status = rig.doJSON(t, http.MethodDelete, "/api/sandbox-secrets/"+created.Id, nil, nil, token)
	if status != http.StatusNotFound {
		t.Errorf("second delete status = %d, want %d (already gone)", status, http.StatusNotFound)
	}
}
