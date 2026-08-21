//go:build integration

// Integration tests for §25.10/§25.11's own ("workflow definition & run
// API", §25.10/§25.11) binding routes -- workflowbindings.go -- against a
// real Postgres instance, sharing this package's own testRig and
// createUserWithRole (planapprove_integration_test.go)/createCustomDefinition
// (workflowdefinitions_integration_test.go).
package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

func TestListWorkflowBindings_IncludesTheThreeSeededGlobals(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	var resp restdtos.ListWorkflowBindingsResponse
	status := rig.doJSON(t, http.MethodGet, "/api/workflow-bindings", nil, &resp, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	seenGlobal := map[string]bool{}
	for _, b := range resp.Bindings {
		if b.RepoFullName == nil {
			seenGlobal[string(b.Lane)] = true
		}
	}
	for _, lane := range []string{"review", "request", "plan"} {
		if !seenGlobal[lane] {
			t.Errorf("no global binding found for lane %q, want the migration-seeded one", lane)
		}
	}
}

func TestListWorkflowBindings_MemberDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t) // default role: member.

	status := rig.doJSON(t, http.MethodGet, "/api/workflow-bindings", nil, nil, token)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

// TestPutWorkflowBinding_RBACMatrix is this Step's own explicitly
// required RBAC test: a maintainer -- who CAN edit an unbound draft
// (workflowdefinitions_integration_test.go's own
// TestPutWorkflowDefinition_MaintainerCanEditUnboundDraft) -- CANNOT
// activate a binding; an admin can. Both render the SAME
// authz.ActionActivateWorkflowBinding verdict (§25.11).
func TestPutWorkflowBinding_RBACMatrix(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, maintainerToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)
	_, adminToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	defID, _ := createCustomDefinition(ctx, t, rig, "request", "rbac-binding-target")

	req := restdtos.PutWorkflowBindingRequest{
		Lane:                 restdtos.PutWorkflowBindingRequestLaneRequest,
		RepoFullName:         nil,
		WorkflowDefinitionId: defID,
	}

	status := rig.doJSON(t, http.MethodPut, "/api/workflow-bindings", mustJSON(t, req), nil, maintainerToken)
	if status != http.StatusForbidden {
		t.Errorf("maintainer PUT status = %d, want %d (activation is admin-only, §25.11)", status, http.StatusForbidden)
	}

	var resp restdtos.WorkflowBinding
	status = rig.doJSON(t, http.MethodPut, "/api/workflow-bindings", mustJSON(t, req), &resp, adminToken)
	if status != http.StatusOK {
		t.Fatalf("admin PUT status = %d, want %d", status, http.StatusOK)
	}
	if resp.WorkflowDefinitionId != defID {
		t.Errorf("WorkflowDefinitionId = %q, want %q", resp.WorkflowDefinitionId, defID)
	}
}

// TestPutWorkflowBinding_SameGlobalTwice_LeavesExactlyOneRow is this
// Step's own explicitly required test: PUTting the SAME global binding
// twice must leave exactly one row -- proving the two-partial-unique-
// index upsert (workflowbindings.go's own top doc comment) actually
// avoids the "ON CONFLICT (lane, repo_full_name) never matches a NULL
// row" trap.
func TestPutWorkflowBinding_SameGlobalTwice_LeavesExactlyOneRow(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	firstDefID, _ := createCustomDefinition(ctx, t, rig, "request", "global-twice-1")
	secondDefID, _ := createCustomDefinition(ctx, t, rig, "request", "global-twice-2")

	put := func(defID string) restdtos.WorkflowBinding {
		req := restdtos.PutWorkflowBindingRequest{Lane: restdtos.PutWorkflowBindingRequestLaneRequest, RepoFullName: nil, WorkflowDefinitionId: defID}
		var resp restdtos.WorkflowBinding
		status := rig.doJSON(t, http.MethodPut, "/api/workflow-bindings", mustJSON(t, req), &resp, token)
		if status != http.StatusOK {
			t.Fatalf("PUT status = %d, want %d", status, http.StatusOK)
		}
		return resp
	}

	first := put(firstDefID)
	second := put(secondDefID) // repoint the SAME global binding at a different definition.

	if first.Id != second.Id {
		t.Errorf("global binding id changed across two PUTs (%s -> %s), want the SAME row updated in place", first.Id, second.Id)
	}
	if second.WorkflowDefinitionId != secondDefID {
		t.Errorf("second PUT's WorkflowDefinitionId = %q, want %q (must actually repoint)", second.WorkflowDefinitionId, secondDefID)
	}

	var count int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM workflow_bindings WHERE lane = 'request' AND repo_full_name IS NULL`).Scan(&count); err != nil {
		t.Fatalf("count global request bindings: %v", err)
	}
	if count != 1 {
		t.Errorf("global request binding row count = %d after two PUTs, want exactly 1", count)
	}
}

func TestPutWorkflowBinding_RepoScoped_RoundTrips(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	defID, _ := createCustomDefinition(ctx, t, rig, "review", "repo-scoped-override")

	repo := "acme/widgets"
	req := restdtos.PutWorkflowBindingRequest{
		Lane:                 restdtos.PutWorkflowBindingRequestLaneReview,
		RepoFullName:         &repo,
		WorkflowDefinitionId: defID,
	}
	var resp restdtos.WorkflowBinding
	status := rig.doJSON(t, http.MethodPut, "/api/workflow-bindings", mustJSON(t, req), &resp, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if resp.RepoFullName == nil || *resp.RepoFullName != repo {
		t.Errorf("RepoFullName = %v, want %q", resp.RepoFullName, repo)
	}

	// The global review binding must be UNAFFECTED (repo override shadows
	// it for this one repo only, §25.4) -- still 1 global + 1 repo row.
	var globalCount, repoCount int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM workflow_bindings WHERE lane = 'review' AND repo_full_name IS NULL`).Scan(&globalCount); err != nil {
		t.Fatalf("count global review bindings: %v", err)
	}
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM workflow_bindings WHERE lane = 'review' AND repo_full_name = $1`, repo).Scan(&repoCount); err != nil {
		t.Fatalf("count repo review bindings: %v", err)
	}
	if globalCount != 1 {
		t.Errorf("global review binding count = %d, want 1 (untouched by the repo-scoped PUT)", globalCount)
	}
	if repoCount != 1 {
		t.Errorf("repo review binding count = %d, want 1", repoCount)
	}
}

func TestPutWorkflowBinding_LaneMismatch_Returns400(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	defID, _ := createCustomDefinition(ctx, t, rig, "review", "lane-mismatch-target")

	req := restdtos.PutWorkflowBindingRequest{
		Lane:                 restdtos.PutWorkflowBindingRequestLaneRequest, // definition is lane "review".
		RepoFullName:         nil,
		WorkflowDefinitionId: defID,
	}
	status := rig.doJSON(t, http.MethodPut, "/api/workflow-bindings", mustJSON(t, req), nil, token)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}
