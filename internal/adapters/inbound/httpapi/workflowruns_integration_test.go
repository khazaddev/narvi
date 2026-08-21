//go:build integration

// Integration tests for §25.10's ("workflow definition & run API", Step
// 88) own two run-read routes -- workflowruns.go -- against a real
// Postgres instance, sharing this package's own testRig,
// createUserWithRole/createSessionForUser (planapprove_integration_test.go),
// and createCustomDefinition (workflowdefinitions_integration_test.go).
package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// mustScanUUID parses s as a pgtype.UUID, failing the test on error.
func mustScanUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan(s); err != nil {
		t.Fatalf("scan uuid %q: %v", s, err)
	}
	return id
}

func TestListSessionWorkflowRuns_UnknownSession_Returns404(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	status := rig.doJSON(t, http.MethodGet, "/api/sessions/00000000-0000-0000-0000-000000000000/workflow-runs", nil, nil, token)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

func TestListSessionWorkflowRuns_NewestFirst(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	owner, token := rig.createAuthenticatedUser(ctx, t)
	session := createSessionForUser(ctx, t, rig, owner.ID, nil)
	defID, _ := createCustomDefinition(ctx, t, rig, "request", "runs-list-def")
	defUUID := mustScanUUID(t, defID)

	run1, err := rig.workflows.CreateRun(ctx, session.ID, "request", defUUID, 1)
	if err != nil {
		t.Fatalf("create run1: %v", err)
	}
	// A session may only have one RUNNING run at a time
	// (workflow_runs_one_running_per_session) -- complete the first
	// before starting the second.
	if _, err := rig.workflows.CompleteRun(ctx, run1.ID); err != nil {
		t.Fatalf("complete run1: %v", err)
	}
	run2, err := rig.workflows.CreateRun(ctx, session.ID, "request", defUUID, 1)
	if err != nil {
		t.Fatalf("create run2: %v", err)
	}

	var resp restdtos.ListWorkflowRunsResponse
	status := rig.doJSON(t, http.MethodGet, "/api/sessions/"+session.ID.String()+"/workflow-runs", nil, &resp, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if len(resp.Runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(resp.Runs))
	}
	if resp.Runs[0].Id != run2.ID.String() || resp.Runs[1].Id != run1.ID.String() {
		t.Errorf("runs = [%s, %s], want [%s, %s] (newest first)", resp.Runs[0].Id, resp.Runs[1].Id, run2.ID.String(), run1.ID.String())
	}
}

func TestListSessionWorkflowRuns_ViewerCanRead(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	owner, _ := rig.createAuthenticatedUser(ctx, t)
	_, viewerToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleViewer)
	session := createSessionForUser(ctx, t, rig, owner.ID, nil)

	status := rig.doJSON(t, http.MethodGet, "/api/sessions/"+session.ID.String()+"/workflow-runs", nil, nil, viewerToken)
	if status != http.StatusOK {
		t.Errorf("status = %d, want %d (session read is allowed to every role including viewer, §13.3 row 1)", status, http.StatusOK)
	}
}

func TestGetWorkflowRun_UnknownRun_Returns404(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	status := rig.doJSON(t, http.MethodGet, "/api/workflow-runs/00000000-0000-0000-0000-000000000000", nil, nil, token)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

// TestGetWorkflowRun_ReturnsStepRunsInOrder is this Step's own explicitly
// required test: "A run read returns its step runs in order." Seeds 3
// sequential attempts of the SAME step (each finished before the next
// starts, honoring workflow_step_runs_one_live_per_run) and asserts the
// response's own stepRuns slice matches creation order exactly.
func TestGetWorkflowRun_ReturnsStepRunsInOrder(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	owner, token := rig.createAuthenticatedUser(ctx, t)
	session := createSessionForUser(ctx, t, rig, owner.ID, nil)
	defID, stepID := createCustomDefinition(ctx, t, rig, "request", "run-detail-order-def")
	defUUID := mustScanUUID(t, defID)
	stepUUID := mustScanUUID(t, stepID)

	run, err := rig.workflows.CreateRun(ctx, session.ID, "request", defUUID, 1)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	var wantOrder []string
	for i := 0; i < 3; i++ {
		sr, err := rig.workflows.CreateStepRun(ctx, run.ID, stepUUID)
		if err != nil {
			t.Fatalf("create step run %d: %v", i, err)
		}
		if _, err := rig.workflows.FinishStepRun(ctx, sr.ID, "completed", "ok"); err != nil {
			t.Fatalf("finish step run %d: %v", i, err)
		}
		wantOrder = append(wantOrder, sr.ID.String())
	}

	var resp restdtos.WorkflowRunDetail
	status := rig.doJSON(t, http.MethodGet, "/api/workflow-runs/"+run.ID.String(), nil, &resp, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if resp.Run.Id != run.ID.String() {
		t.Errorf("Run.Id = %q, want %q", resp.Run.Id, run.ID.String())
	}
	if len(resp.StepRuns) != len(wantOrder) {
		t.Fatalf("got %d step runs, want %d", len(resp.StepRuns), len(wantOrder))
	}
	for i, sr := range resp.StepRuns {
		if sr.Id != wantOrder[i] {
			t.Errorf("stepRuns[%d].Id = %q, want %q (creation order)", i, sr.Id, wantOrder[i])
		}
	}
}
