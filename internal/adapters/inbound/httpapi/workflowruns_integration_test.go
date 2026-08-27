//go:build integration

// Integration tests for §25.10's own two run-read routes --
// workflowruns.go -- against a real
// Postgres instance, sharing this package's own testRig,
// createUserWithRole/createSessionForUser (planapprove_integration_test.go),
// and createCustomDefinition (workflowdefinitions_integration_test.go).
package httpapi_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
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

// getRawGET issues an authenticated GET and returns the raw response body
// bytes alongside the status code -- mirrors
// TestGetWorkflowDefinitions_EdgesAreAlwaysAnArrayOnTheWire's own inline
// idiom (workflowdefinitions_integration_test.go), factored out since
// this file's own two RAW-bytes tests below both need it.
func getRawGET(t *testing.T, rig testRig, path, token string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rig.server.URL+path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: platform.AuthSessionCookieName, Value: token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, raw
}

// TestGetWorkflowRun_StepRun_NoTurnYet_ModelAndCostNullOnTheWire asserts on
// the RAW response bytes, not a decoded struct -- mirrors
// TestGetWorkflowDefinitions_EdgesAreAlwaysAnArrayOnTheWire's own exact
// reasoning (workflowdefinitions_integration_test.go): §25.15 requires
// "no cost yet" to never render as a fabricated 0, and decoding into
// restdtos.WorkflowStepRun and comparing CostUsd == nil would pass
// identically whether the server actually sent `"costUsd":null` or some
// other absent-key shape entirely -- only the literal bytes prove which
// one a client genuinely receives.
func TestGetWorkflowRun_StepRun_NoTurnYet_ModelAndCostNullOnTheWire(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	owner, token := rig.createAuthenticatedUser(ctx, t)
	session := createSessionForUser(ctx, t, rig, owner.ID, nil)
	defID, stepID := createCustomDefinition(ctx, t, rig, "request", "run-detail-no-turn-def")
	defUUID := mustScanUUID(t, defID)
	stepUUID := mustScanUUID(t, stepID)

	run, err := rig.workflows.CreateRun(ctx, session.ID, "request", defUUID, 1)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	// No AttachTurn call: this attempt has no turn yet, exactly the
	// hitlBefore-gated awaiting_decision shape §25.15/the schema's own
	// turnId doc comment describes.
	if _, err := rig.workflows.CreateStepRun(ctx, run.ID, stepUUID); err != nil {
		t.Fatalf("create step run: %v", err)
	}

	status, raw := getRawGET(t, rig, "/api/workflow-runs/"+run.ID.String(), token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d\nbody: %s", status, http.StatusOK, raw)
	}

	if !bytes.Contains(raw, []byte(`"modelId":null`)) {
		t.Errorf("response does not contain \"modelId\":null for a step run with no turn attached yet.\nbody: %s", raw)
	}
	if !bytes.Contains(raw, []byte(`"costUsd":null`)) {
		t.Errorf("response does not contain \"costUsd\":null for a step run with no turn attached yet.\nbody: %s", raw)
	}
}

// TestGetWorkflowRun_StepRun_TurnAttached_ModelAndCostReflectTurnRAWBytes
// proves the positive half of the same contract: once a turn is attached
// and carries a real model/cost, those exact values -- not null, not a
// rounded/coerced approximation -- appear verbatim on the wire, again
// checked on the raw bytes so the assertion is not vacuously satisfied by
// whatever a decoded struct's own zero value happens to be.
func TestGetWorkflowRun_StepRun_TurnAttached_ModelAndCostReflectTurnRAWBytes(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	owner, token := rig.createAuthenticatedUser(ctx, t)
	session := createSessionForUser(ctx, t, rig, owner.ID, nil)
	defID, stepID := createCustomDefinition(ctx, t, rig, "request", "run-detail-turn-attached-def")
	defUUID := mustScanUUID(t, defID)
	stepUUID := mustScanUUID(t, stepID)

	run, err := rig.workflows.CreateRun(ctx, session.ID, "request", defUUID, 1)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	sr, err := rig.workflows.CreateStepRun(ctx, run.ID, stepUUID)
	if err != nil {
		t.Fatalf("create step run: %v", err)
	}

	modelID := "anthropic/claude-opus-4"
	turn, err := rig.turns.Create(ctx, sqlcgen.CreateTurnParams{
		SessionID: session.ID,
		Status:    sqlcgen.TurnStatusCompleted,
		ModelID:   &modelID,
	})
	if err != nil {
		t.Fatalf("create turn: %v", err)
	}
	if err := rig.workflows.AttachTurn(ctx, sr.ID, turn.ID); err != nil {
		t.Fatalf("attach turn: %v", err)
	}
	// Sets cost_usd directly rather than through the real step_finish event
	// path (already proven end to end by internal/app/sessionactor's own
	// stepcost_integration_test.go) -- this test's own job is only to
	// prove the READ side (the join + wire rendering), not re-prove the
	// WRITE side a second time.
	if _, err := rig.pool.Exec(ctx, `UPDATE turns SET cost_usd = 1.23 WHERE id = $1`, turn.ID); err != nil {
		t.Fatalf("set turn cost_usd: %v", err)
	}

	status, raw := getRawGET(t, rig, "/api/workflow-runs/"+run.ID.String(), token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d\nbody: %s", status, http.StatusOK, raw)
	}

	if !bytes.Contains(raw, []byte(`"modelId":"anthropic/claude-opus-4"`)) {
		t.Errorf("response does not contain the attached turn's own model id verbatim.\nbody: %s", raw)
	}
	if !bytes.Contains(raw, []byte(`"costUsd":1.23`)) {
		t.Errorf("response does not contain the attached turn's own cost_usd verbatim.\nbody: %s", raw)
	}
}
