//go:build integration

// Integration tests for Step 61's ("domain/turn: builder epistemic
// pre-action check", §20.2) own structured-signal-reporting tool
// (epistemicoutcome.go), against a real Postgres instance -- sharing this
// package's own testRig (httpapi_integration_test.go), mirroring
// workflowstepoutcome_integration_test.go's own house style exactly (both
// are sandbox-bearer-authenticated tools posting onto whichever attempt is
// currently live for the calling session, no id supplied by the caller).
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/inbound/httpapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

func validEpistemicOutcomeRequestJSON(outcome string) string {
	return `{"outcome":"` + outcome + `"}`
}

// postEpistemicOutcome posts body to sessionID's own
// turn/epistemic-outcome endpoint, mirroring postWorkflowStepOutcome's own
// identical bearer/gen header convention.
func postEpistemicOutcome(t *testing.T, r testRig, sessionID, bearer, gen, body string) (int, restdtos.PostEpistemicOutcomeResponse) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, r.server.URL+"/sessions/"+sessionID+"/turn/epistemic-outcome", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
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

	var got restdtos.PostEpistemicOutcomeResponse
	_ = json.NewDecoder(resp.Body).Decode(&got)
	return resp.StatusCode, got
}

// createProcessingTurn creates a real turn via CreateTurnCore (so it goes
// through the SAME core every production caller does), then forces it
// directly into 'processing' -- bypassing the real dispatch pipeline,
// mirroring TestPostWorkflowStepOutcome_SecondCallAfterStepFinished_
// BadRequest's own identical "force ... directly" precedent
// (workflowstepoutcome_integration_test.go): this test only needs "this
// IS the live processing turn", not a faithful re-derivation of how a
// real sandbox connection would get it there.
func createProcessingTurn(ctx context.Context, t *testing.T, r testRig, sessionID pgtype.UUID, planMode bool) sqlcgen.Turn {
	t.Helper()
	created, _, cerr := httpapi.CreateTurnCore(ctx, r.pool, r.sessions, r.turns, r.plans, r.auditLog, r.registry, sessionID, "do the thing", nil, planMode, false, pgtype.UUID{}, httpapi.RejectIfOpen)
	if cerr != nil {
		t.Fatalf("CreateTurnCore: status=%d message=%q", cerr.Status, cerr.Message)
	}
	if _, err := r.pool.Exec(ctx, `UPDATE turns SET status = 'processing' WHERE id = $1`, created.ID); err != nil {
		t.Fatalf("force turn processing: %v", err)
	}
	created.Status = sqlcgen.TurnStatusProcessing
	return created
}

func TestPostEpistemicOutcome_NotFound(t *testing.T) {
	rig := newTestRig(t)

	status, _ := postEpistemicOutcome(t, rig, "00000000-0000-0000-0000-000000000000", "any-token", "1", validEpistemicOutcomeRequestJSON("none"))
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

func TestPostEpistemicOutcome_MissingBearer_Unauthorized(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := bareSessionWithSandbox(ctx, t, rig, "epistemic-nobearer")

	status, _ := postEpistemicOutcome(t, rig, session.ID.String(), "", "1", validEpistemicOutcomeRequestJSON("none"))
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

func TestPostEpistemicOutcome_WrongToken_Unauthorized(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := bareSessionWithSandbox(ctx, t, rig, "epistemic-wrongtoken")

	status, _ := postEpistemicOutcome(t, rig, session.ID.String(), "not-the-real-token", "1", validEpistemicOutcomeRequestJSON("none"))
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

func TestPostEpistemicOutcome_GenMismatch_Forbidden(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := bareSessionWithSandbox(ctx, t, rig, "epistemic-genmismatch")

	status, _ := postEpistemicOutcome(t, rig, session.ID.String(), "epistemic-genmismatch", "999", validEpistemicOutcomeRequestJSON("none"))
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

// TestPostEpistemicOutcome_MalformedBody_BadRequest proves an outcome
// value outside the closed {none, minor, strong} vocabulary is rejected
// at decode time (restdtos.PostEpistemicOutcomeRequest's own generated
// UnmarshalJSON), never reaching the domain layer at all.
func TestPostEpistemicOutcome_MalformedBody_BadRequest(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := bareSessionWithSandbox(ctx, t, rig, "epistemic-malformed")

	status, _ := postEpistemicOutcome(t, rig, session.ID.String(), "epistemic-malformed", "1", `{"outcome":"not-a-real-outcome"}`)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

// TestPostEpistemicOutcome_NoProcessingTurn_BadRequest proves the
// "meaningless call" 400: a session that has never dispatched any turn at
// all has no processing turn to post onto.
func TestPostEpistemicOutcome_NoProcessingTurn_BadRequest(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := bareSessionWithSandbox(ctx, t, rig, "epistemic-noturn")

	status, _ := postEpistemicOutcome(t, rig, session.ID.String(), "epistemic-noturn", "1", validEpistemicOutcomeRequestJSON("none"))
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

// TestPostEpistemicOutcome_Success_RecordsOnProcessingTurn is the
// flagship happy-path proof, covering all three EpistemicOutcome values
// (§20.2's own closed vocabulary) -- mirrors internal/domain/turn.
// AllEpistemicOutcomes' own exhaustive-value discipline, one layer up at
// the endpoint.
func TestPostEpistemicOutcome_Success_RecordsOnProcessingTurn(t *testing.T) {
	for _, outcome := range []string{"none", "minor", "strong"} {
		t.Run(outcome, func(t *testing.T) {
			rig := newTestRig(t)
			ctx := context.Background()
			session := bareSessionWithSandbox(ctx, t, rig, "epistemic-success-"+outcome)
			turn := createProcessingTurn(ctx, t, rig, session.ID, false)

			status, resp := postEpistemicOutcome(t, rig, session.ID.String(), "epistemic-success-"+outcome, "1", validEpistemicOutcomeRequestJSON(outcome))
			if status != http.StatusCreated {
				t.Fatalf("status = %d, want %d", status, http.StatusCreated)
			}
			if resp.TurnId != turn.ID.String() {
				t.Fatalf("response.TurnId = %q, want %q", resp.TurnId, turn.ID.String())
			}

			var gotOutcome string
			if err := rig.pool.QueryRow(ctx, `SELECT epistemic_outcome FROM turns WHERE id = $1`, turn.ID).Scan(&gotOutcome); err != nil {
				t.Fatalf("query turn: %v", err)
			}
			if gotOutcome != outcome {
				t.Errorf("turns.epistemic_outcome = %q, want %q", gotOutcome, outcome)
			}
		})
	}
}

// TestPostEpistemicOutcome_AbsentUntilPosted proves absent (NULL) and
// EpistemicOutcomeNone ("posted, found nothing") stay distinguishable at
// the storage layer (§20.2, internal/domain/turn.EpistemicOutcome's own
// doc comment) -- a freshly created, still-pending turn (this endpoint
// never even reachable yet, since it is not the live processing turn) has
// NULL epistemic_outcome, not the string "none".
func TestPostEpistemicOutcome_AbsentUntilPosted(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := bareSessionWithSandbox(ctx, t, rig, "epistemic-absent")

	created, _, cerr := httpapi.CreateTurnCore(ctx, rig.pool, rig.sessions, rig.turns, rig.plans, rig.auditLog, rig.registry, session.ID, "do the thing", nil, false, false, pgtype.UUID{}, httpapi.RejectIfOpen)
	if cerr != nil {
		t.Fatalf("CreateTurnCore: status=%d message=%q", cerr.Status, cerr.Message)
	}

	var gotOutcome *string
	if err := rig.pool.QueryRow(ctx, `SELECT epistemic_outcome FROM turns WHERE id = $1`, created.ID).Scan(&gotOutcome); err != nil {
		t.Fatalf("query turn: %v", err)
	}
	if gotOutcome != nil {
		t.Errorf("turns.epistemic_outcome = %q, want NULL (absent, the check never ran) -- not the string \"none\"", *gotOutcome)
	}
}

// TestPostEpistemicOutcome_PlanModeProcessingTurn_BadRequest proves this
// endpoint's own defense-in-depth (§20.3, epistemicoutcome.go's own doc
// comment): even if a plan-mode turn somehow IS the live processing turn
// and an agent calls this endpoint anyway, the server refuses rather than
// recording an outcome for a turn class the epistemic check never applies
// to in the first place.
func TestPostEpistemicOutcome_PlanModeProcessingTurn_BadRequest(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := bareSessionWithSandbox(ctx, t, rig, "epistemic-planmode")
	createProcessingTurn(ctx, t, rig, session.ID, true)

	status, _ := postEpistemicOutcome(t, rig, session.ID.String(), "epistemic-planmode", "1", validEpistemicOutcomeRequestJSON("none"))
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

// TestPostEpistemicOutcome_SecondCallAfterTurnFinished_BadRequest mirrors
// TestPostWorkflowStepOutcome_SecondCallAfterStepFinished_BadRequest's own
// identical reasoning exactly, one layer down (a turn, not a workflow
// step run): once the turn this endpoint originally targeted has gone
// terminal, GetProcessingTurnForSession (step 7 of this handler's own
// outcome table) finds no processing turn at all and reports the SAME 400
// a session with no processing turn ever at all gets. The narrower 409
// (step 8, the guarded UPDATE's own write-time race) is reserved for the
// tighter race where the read still sees a live turn but the write itself
// loses to a concurrent transition in the microseconds after -- not
// exercised here, mirroring the workflow-step-outcome test's own identical
// scope decision.
func TestPostEpistemicOutcome_SecondCallAfterTurnFinished_BadRequest(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := bareSessionWithSandbox(ctx, t, rig, "epistemic-conflict")
	turn := createProcessingTurn(ctx, t, rig, session.ID, false)

	status, _ := postEpistemicOutcome(t, rig, session.ID.String(), "epistemic-conflict", "1", validEpistemicOutcomeRequestJSON("minor"))
	if status != http.StatusCreated {
		t.Fatalf("first post: status = %d, want %d", status, http.StatusCreated)
	}

	if _, err := rig.pool.Exec(ctx, `UPDATE turns SET status = 'completed' WHERE id = $1`, turn.ID); err != nil {
		t.Fatalf("force turn terminal: %v", err)
	}

	status2, _ := postEpistemicOutcome(t, rig, session.ID.String(), "epistemic-conflict", "1", validEpistemicOutcomeRequestJSON("strong"))
	if status2 != http.StatusBadRequest {
		t.Errorf("second post: status = %d, want %d", status2, http.StatusBadRequest)
	}

	// The FIRST post's own outcome ("minor") must survive unclobbered --
	// the second, rejected call never reaches the guarded UPDATE at all.
	var gotOutcome string
	if err := rig.pool.QueryRow(ctx, `SELECT epistemic_outcome FROM turns WHERE id = $1`, turn.ID).Scan(&gotOutcome); err != nil {
		t.Fatalf("query turn: %v", err)
	}
	if gotOutcome != "minor" {
		t.Errorf("turns.epistemic_outcome = %q, want %q (first post's own value, unclobbered)", gotOutcome, "minor")
	}
}

// TestSetEpistemicOutcome_GuardedUpdate_NoLongerProcessing_ZeroRows proves
// the guarded UPDATE itself (TurnStore.SetEpistemicOutcome, "AND
// status = 'processing'", queries/turns.sql) directly -- calling the
// store method itself, bypassing the HTTP endpoint's own step-7 read-time
// check entirely. TestPostEpistemicOutcome_SecondCallAfterTurnFinished_
// BadRequest above never actually reaches this guard (step 7's own read
// already returns 400 first, before the guarded UPDATE ever runs -- see
// that test's own doc comment for why, mirroring the workflow-step-
// outcome test's identical scope decision) -- this test closes that gap
// by exercising the write-time guard on its own terms: a turn that is no
// longer 'processing' gets 0 rows affected, never a silent overwrite of
// a terminal turn's own outcome.
func TestSetEpistemicOutcome_GuardedUpdate_NoLongerProcessing_ZeroRows(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := bareSessionWithSandbox(ctx, t, rig, "epistemic-guarded-update")
	turn := createProcessingTurn(ctx, t, rig, session.ID, false)

	if _, err := rig.pool.Exec(ctx, `UPDATE turns SET status = 'completed' WHERE id = $1`, turn.ID); err != nil {
		t.Fatalf("force turn terminal: %v", err)
	}

	rowsAffected, err := rig.turns.SetEpistemicOutcome(ctx, turn.ID, sqlcgen.TurnEpistemicOutcomeStrong)
	if err != nil {
		t.Fatalf("SetEpistemicOutcome: %v", err)
	}
	if rowsAffected != 0 {
		t.Errorf("SetEpistemicOutcome(...) rowsAffected = %d, want 0 (turn is no longer processing)", rowsAffected)
	}

	var gotOutcome *string
	if err := rig.pool.QueryRow(ctx, `SELECT epistemic_outcome FROM turns WHERE id = $1`, turn.ID).Scan(&gotOutcome); err != nil {
		t.Fatalf("query turn: %v", err)
	}
	if gotOutcome != nil {
		t.Errorf("turns.epistemic_outcome = %v, want still NULL (the guarded update must not have written anything for a non-processing turn)", *gotOutcome)
	}
}
