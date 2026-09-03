//go:build integration

// Integration tests for GET /api/environments (environments.go's own
// ListEnvironments), against a real Postgres instance -- sharing this
// package's own testRig (httpapi_integration_test.go).
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/narvidev/narvi/contracts/gen/go/restdtos"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// TestListEnvironments_MemberDenied proves the real server-side gate: a
// member (below authz.ActionManageEnvironments' admin/maintainer floor)
// gets a genuine 403 from the endpoint itself, never merely a
// client-side-hidden affordance.
func TestListEnvironments_MemberDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMember)

	status := rig.doJSON(t, http.MethodGet, "/api/environments", nil, nil, token)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", status, http.StatusForbidden)
	}
}

// TestListEnvironments_ViewerDenied mirrors the member case above -- a
// viewer sits even further below the floor.
func TestListEnvironments_ViewerDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleViewer)

	status := rig.doJSON(t, http.MethodGet, "/api/environments", nil, nil, token)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", status, http.StatusForbidden)
	}
}

// TestListEnvironments_MaintainerAllowed_RendersRealColumnsOnly seeds one
// environments row directly (path_scope + docker_required + an allowlist
// egress policy) and proves a maintainer sees it back with EXACTLY the
// columns that exist -- no name, no repo list, no image-build status (see
// environments.go's own doc comment for why those are declined).
func TestListEnvironments_MaintainerAllowed_RendersRealColumnsOnly(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	pathScope, err := json.Marshal([]string{"web/**", "contracts/api/**"})
	if err != nil {
		t.Fatalf("marshal pathScope: %v", err)
	}
	allowlist, err := json.Marshal([]string{"registry.example.invalid"})
	if err != nil {
		t.Fatalf("marshal allowlist: %v", err)
	}
	mode := "allowlist"
	row, err := rig.environments.Create(ctx, sqlcgen.CreateEnvironmentParams{
		PathScope:             pathScope,
		MockConfigured:        true,
		DockerRequired:        true,
		EgressPolicyMode:      &mode,
		EgressPolicyAllowlist: allowlist,
	})
	if err != nil {
		t.Fatalf("seed environment: %v", err)
	}

	var resp restdtos.ListEnvironmentsResponse
	status := rig.doJSON(t, http.MethodGet, "/api/environments", nil, &resp, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	found := false
	for _, env := range resp.Environments {
		if env.Id != row.ID.String() {
			continue
		}
		found = true
		if !env.MockConfigured {
			t.Errorf("MockConfigured = false, want true")
		}
		if !env.DockerRequired {
			t.Errorf("DockerRequired = false, want true")
		}
		if env.PathScope == nil || len(*env.PathScope) != 2 {
			t.Errorf("PathScope = %v, want 2 entries", env.PathScope)
		}
		if env.EgressPolicyMode == nil || env.EgressPolicyMode.Value != "allowlist" {
			t.Errorf("EgressPolicyMode = %v, want allowlist", env.EgressPolicyMode)
		}
		if env.EgressPolicyAllowlist == nil || len(*env.EgressPolicyAllowlist) != 1 {
			t.Errorf("EgressPolicyAllowlist = %v, want 1 entry", env.EgressPolicyAllowlist)
		}
	}
	if !found {
		t.Fatalf("seeded environment %s not present in response: %+v", row.ID.String(), resp.Environments)
	}
}
