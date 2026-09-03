//go:build integration

// Integration tests for §27.1's own ("sandbox secrets & opencode
// config", §27.2) CP-side management surface (opencodeconfig.go),
// against a real Postgres instance -- sharing this package's own testRig
// (httpapi_integration_test.go).
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/narvidev/narvi/contracts/gen/go/restdtos"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// --- RBAC ---

func TestPutEnvironmentOpenCodeConfig_MemberDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	status := rig.doJSON(t, http.MethodPut, "/api/environments/11111111-1111-1111-1111-111111111111/opencode-config",
		[]byte(`{"document":{"model":"anthropic/claude"}}`), nil, token)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

func TestPutEnvironmentOpenCodeConfig_MaintainerAllowed(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)
	env, err := rig.environments.Create(ctx, sqlcgen.CreateEnvironmentParams{PathScope: []byte(`[]`)})
	if err != nil {
		t.Fatalf("create environment: %v", err)
	}

	var got restdtos.OpenCodeConfig
	status := rig.doJSON(t, http.MethodPut, "/api/environments/"+env.ID.String()+"/opencode-config",
		[]byte(`{"document":{"model":"anthropic/claude"}}`), &got, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got.Scope != restdtos.OpenCodeConfigScopeEnvironment {
		t.Errorf("Scope = %q, want %q", got.Scope, restdtos.OpenCodeConfigScopeEnvironment)
	}
}

func TestPutGlobalOpenCodeConfig_MaintainerDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	status := rig.doJSON(t, http.MethodPut, "/api/opencode-config",
		[]byte(`{"document":{"autoupdate":true}}`), nil, token)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d (admin only, no maintainer carve-out)", status, http.StatusForbidden)
	}
}

func TestPutGlobalOpenCodeConfig_AdminAllowed(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	var got restdtos.OpenCodeConfig
	status := rig.doJSON(t, http.MethodPut, "/api/opencode-config",
		[]byte(`{"document":{"autoupdate":true}}`), &got, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got.Scope != restdtos.OpenCodeConfigScopeGlobal {
		t.Errorf("Scope = %q, want %q", got.Scope, restdtos.OpenCodeConfigScopeGlobal)
	}
}

// --- Validation: must be a JSON object ---

func TestPutGlobalOpenCodeConfig_NonObjectDocument_Rejected(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	tests := []struct {
		name string
		body string
	}{
		{"array", `{"document":["not","an","object"]}`},
		{"string", `{"document":"not an object"}`},
		{"number", `{"document":42}`},
		{"null", `{"document":null}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status := rig.doJSON(t, http.MethodPut, "/api/opencode-config", []byte(tc.body), nil, token)
			if status != http.StatusBadRequest {
				t.Errorf("status = %d, want %d (document must be a JSON object)", status, http.StatusBadRequest)
			}
		})
	}
}

// --- Full document returned (NOT write-only, unlike secrets) ---

func TestGetGlobalOpenCodeConfig_ReturnsFullDocument(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	status := rig.doJSON(t, http.MethodPut, "/api/opencode-config",
		[]byte(`{"document":{"model":"anthropic/claude-sonnet","autoupdate":true}}`), nil, token)
	if status != http.StatusOK {
		t.Fatalf("put status = %d, want %d", status, http.StatusOK)
	}

	var got restdtos.OpenCodeConfig
	status = rig.doJSON(t, http.MethodGet, "/api/opencode-config", nil, &got, token)
	if status != http.StatusOK {
		t.Fatalf("get status = %d, want %d", status, http.StatusOK)
	}
	var doc map[string]any
	if err := json.Unmarshal(got.Document, &doc); err != nil {
		t.Fatalf("unmarshal document: %v", err)
	}
	if doc["model"] != "anthropic/claude-sonnet" || doc["autoupdate"] != true {
		t.Errorf("document = %+v, want it to contain model/autoupdate exactly as submitted", doc)
	}
}

// --- Not-yet-configured is 404, never an error ---

func TestGetGlobalOpenCodeConfig_NotConfigured_404(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	status := rig.doJSON(t, http.MethodGet, "/api/opencode-config", nil, nil, token)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

// --- Upsert replaces in place ---

func TestPutGlobalOpenCodeConfig_SecondPutReplaces(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	status := rig.doJSON(t, http.MethodPut, "/api/opencode-config", []byte(`{"document":{"model":"first"}}`), nil, token)
	if status != http.StatusOK {
		t.Fatalf("first put status = %d, want %d", status, http.StatusOK)
	}
	status = rig.doJSON(t, http.MethodPut, "/api/opencode-config", []byte(`{"document":{"model":"second"}}`), nil, token)
	if status != http.StatusOK {
		t.Fatalf("second put status = %d, want %d", status, http.StatusOK)
	}

	var got restdtos.OpenCodeConfig
	status = rig.doJSON(t, http.MethodGet, "/api/opencode-config", nil, &got, token)
	if status != http.StatusOK {
		t.Fatalf("get status = %d, want %d", status, http.StatusOK)
	}
	var doc map[string]any
	if err := json.Unmarshal(got.Document, &doc); err != nil {
		t.Fatalf("unmarshal document: %v", err)
	}
	if doc["model"] != "second" {
		t.Errorf("document.model = %v, want %q (second PUT must replace, not merge/duplicate)", doc["model"], "second")
	}
}

// --- Delete ---

func TestDeleteGlobalOpenCodeConfig_RemovesRow(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	status := rig.doJSON(t, http.MethodPut, "/api/opencode-config", []byte(`{"document":{"a":true}}`), nil, token)
	if status != http.StatusOK {
		t.Fatalf("put status = %d, want %d", status, http.StatusOK)
	}
	status = rig.doJSON(t, http.MethodDelete, "/api/opencode-config", nil, nil, token)
	if status != http.StatusNoContent {
		t.Fatalf("delete status = %d, want %d", status, http.StatusNoContent)
	}
	status = rig.doJSON(t, http.MethodGet, "/api/opencode-config", nil, nil, token)
	if status != http.StatusNotFound {
		t.Errorf("get after delete status = %d, want %d", status, http.StatusNotFound)
	}
}
