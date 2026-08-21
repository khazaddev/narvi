//go:build integration

// Integration tests for the automations UI's own GET /api/automations/
// {automationID}/invocations (automationinvocations.go) -- the "runs
// table" half of mockups.html's own Automations view, sharing this
// package's own testRig (httpapi_integration_test.go).
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// createManualAutomation seeds a minimal, real automation row (manual
// trigger, one target repo) directly via the store -- this file's own
// tests care about the invocations/runs read model, not automation
// creation itself (already covered by automations_integration_test.go).
func createManualAutomation(ctx context.Context, t *testing.T, r testRig, ownerID pgtype.UUID) sqlcgen.Automation {
	t.Helper()
	repos, err := json.Marshal([]map[string]any{{"name": "widgets", "url": "https://github.com/acme/widgets", "branch": nil}})
	if err != nil {
		t.Fatalf("marshal repos: %v", err)
	}
	a, err := r.automations.Create(ctx, sqlcgen.CreateAutomationParams{
		Name:          "nightly audit",
		Repos:         repos,
		CreatedBy:     ownerID,
		TriggerType:   sqlcgen.AutomationTriggerTypeManual,
		TriggerConfig: []byte(`{}`),
		EnvVars:       []byte(`[]`),
	})
	if err != nil {
		t.Fatalf("create automation: %v", err)
	}
	return a
}

// TestListAutomationInvocations_NotFound proves a nonexistent automation
// id 404s, mirroring GetAutomation's own identical precedent.
func TestListAutomationInvocations_NotFound(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	status := rig.doJSON(t, http.MethodGet, "/api/automations/00000000-0000-0000-0000-000000000000/invocations", nil, nil, token)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
}

// TestListAutomationInvocations_EveryRoleCanRead proves this route has no
// extra RBAC beyond "must be logged in" -- mirrors ListAutomations/
// GetAutomation's own identical precedent (a viewer may read, even though
// only admin/maintainer may CREATE an automation).
func TestListAutomationInvocations_EveryRoleCanRead(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	owner, _ := rig.createAuthenticatedUser(ctx, t)
	automation := createManualAutomation(ctx, t, rig, owner.ID)

	_, viewerToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleViewer)

	var resp restdtos.ListAutomationInvocationsResponse
	status := rig.doJSON(t, http.MethodGet, "/api/automations/"+automation.ID.String()+"/invocations", nil, &resp, viewerToken)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (viewer should be able to read)", status)
	}
	if len(resp.Invocations) != 0 {
		t.Errorf("Invocations = %v, want empty (no invocations seeded yet)", resp.Invocations)
	}
}

// TestListAutomationInvocations_ReturnsNestedRunsNewestFirst is this
// route's own central shape proof: two invocations, the second (and its
// run) seeded after the first, come back newest-first, each carrying its
// own real run rows -- target/status/sessionId -- with no fabricated
// per-run narrative text (see automationinvocations.go's own top doc
// comment for why: automation_runs has no such column).
func TestListAutomationInvocations_ReturnsNestedRunsNewestFirst(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	owner, token := rig.createAuthenticatedUser(ctx, t)
	automation := createManualAutomation(ctx, t, rig, owner.ID)
	runSession := rig.createSession(ctx, t)

	target, err := json.Marshal(map[string]any{"name": "widgets", "url": "https://github.com/acme/widgets", "branch": nil})
	if err != nil {
		t.Fatalf("marshal target: %v", err)
	}

	inv1, err := rig.automationInvocations.Create(ctx, sqlcgen.CreateAutomationInvocationParams{
		AutomationID: automation.ID,
		Targets:      []byte(`[{"name":"widgets","url":"https://github.com/acme/widgets","branch":null}]`),
		TotalRuns:    1,
	})
	if err != nil {
		t.Fatalf("create invocation1: %v", err)
	}
	run1, err := rig.automationRuns.Create(ctx, sqlcgen.CreateAutomationRunParams{
		InvocationID: inv1.ID,
		AutomationID: automation.ID,
		Target:       target,
		Status:       sqlcgen.AutomationRunStatusFailed,
		SessionID:    runSession.ID,
	})
	if err != nil {
		t.Fatalf("create run1: %v", err)
	}

	inv2, err := rig.automationInvocations.Create(ctx, sqlcgen.CreateAutomationInvocationParams{
		AutomationID: automation.ID,
		Targets:      []byte(`[{"name":"widgets","url":"https://github.com/acme/widgets","branch":null}]`),
		TotalRuns:    1,
	})
	if err != nil {
		t.Fatalf("create invocation2: %v", err)
	}
	run2, err := rig.automationRuns.Create(ctx, sqlcgen.CreateAutomationRunParams{
		InvocationID: inv2.ID,
		AutomationID: automation.ID,
		Target:       target,
		Status:       sqlcgen.AutomationRunStatusSucceeded,
	})
	if err != nil {
		t.Fatalf("create run2: %v", err)
	}

	var resp restdtos.ListAutomationInvocationsResponse
	status := rig.doJSON(t, http.MethodGet, "/api/automations/"+automation.ID.String()+"/invocations", nil, &resp, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(resp.Invocations) != 2 {
		t.Fatalf("len(Invocations) = %d, want 2", len(resp.Invocations))
	}

	// Newest first: inv2 before inv1.
	if resp.Invocations[0].Id != inv2.ID.String() {
		t.Errorf("Invocations[0].Id = %q, want inv2 %q (newest first)", resp.Invocations[0].Id, inv2.ID.String())
	}
	if resp.Invocations[1].Id != inv1.ID.String() {
		t.Errorf("Invocations[1].Id = %q, want inv1 %q", resp.Invocations[1].Id, inv1.ID.String())
	}

	firstInv := resp.Invocations[0]
	if len(firstInv.Runs) != 1 {
		t.Fatalf("Invocations[0].Runs len = %d, want 1", len(firstInv.Runs))
	}
	gotRun2 := firstInv.Runs[0]
	if gotRun2.Id != run2.ID.String() {
		t.Errorf("run id = %q, want %q", gotRun2.Id, run2.ID.String())
	}
	if gotRun2.Status != restdtos.AutomationRunStatusSucceeded {
		t.Errorf("run2 status = %q, want succeeded", gotRun2.Status)
	}
	if gotRun2.Target.Name != "widgets" {
		t.Errorf("run2 target.Name = %q, want %q", gotRun2.Target.Name, "widgets")
	}
	if gotRun2.SessionId != nil {
		t.Errorf("run2 SessionId = %v, want nil (no session was set for run2)", gotRun2.SessionId)
	}

	secondInv := resp.Invocations[1]
	if len(secondInv.Runs) != 1 {
		t.Fatalf("Invocations[1].Runs len = %d, want 1", len(secondInv.Runs))
	}
	gotRun1 := secondInv.Runs[0]
	if gotRun1.Id != run1.ID.String() {
		t.Errorf("run id = %q, want %q", gotRun1.Id, run1.ID.String())
	}
	if gotRun1.Status != restdtos.AutomationRunStatusFailed {
		t.Errorf("run1 status = %q, want failed", gotRun1.Status)
	}
	if gotRun1.SessionId == nil || *gotRun1.SessionId != runSession.ID.String() {
		t.Errorf("run1 SessionId = %v, want %q", gotRun1.SessionId, runSession.ID.String())
	}
}
