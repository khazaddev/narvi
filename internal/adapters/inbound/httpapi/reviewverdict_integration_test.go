//go:build integration

// Integration tests for Step 47's ("server-side verdict", §8.2/§5.2/§21.2)
// own verdict-posting tool (reviewverdict.go), against a real Postgres
// instance -- gated behind the "integration" build tag, sharing this
// package's own testRig (httpapi_integration_test.go).
package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/domain/reviewpost"
)

// validVerdictRequestJSON is the happy-path request body every test in
// this file starts from, mutating only the ONE field a given test cares
// about -- RiskLevel low, Premise ok, empty BlastRadius (legal), adequate
// coverage, no docs drift, the model proposing "auto" (irrelevant to what
// the server actually computes), and a real, non-blank Summary.
func validVerdictRequestJSON() string {
	return `{
		"riskLevel": "low",
		"premise": "ok",
		"blastRadius": [],
		"filesChanged": 3,
		"testsCoverage": "adequate",
		"docsDrift": "none",
		"proposedShippable": "auto",
		"summary": "Looks good overall, one minor nit below."
	}`
}

// postReviewVerdict posts body to sessionID's own review/verdict endpoint,
// with bearer/gen as the SAME sandbox-bearer-auth headers scmcredentials_
// integration_test.go's own postScmCredentialsFull establishes -- gen ==
// "" omits the X-Sandbox-Gen header entirely (matching a real caller that
// never sends it), mirroring that helper's own identical convention.
func postReviewVerdict(t *testing.T, r testRig, sessionID, bearer, gen, body string) (int, restdtos.PostReviewVerdictResponse) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, r.server.URL+"/sessions/"+sessionID+"/review/verdict", strings.NewReader(body))
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

	var got restdtos.PostReviewVerdictResponse
	_ = json.NewDecoder(resp.Body).Decode(&got)
	return resp.StatusCode, got
}

// setupReviewSessionWithSandbox creates an owned GitHub review session
// (createOwnedGitHubReviewSession, reviewretrigger_integration_test.go)
// plus a real sandbox row with a known bearer token (createSandboxWithToken,
// scmcredentials_integration_test.go) -- the fixture every happy/auth-path
// test in this file needs, reusing both existing helpers rather than a
// third, duplicated setup routine.
func setupReviewSessionWithSandbox(ctx context.Context, t *testing.T, r testRig, repoFullName string, prNumber int32) sqlcgen.Session {
	t.Helper()
	owner, _ := r.createAuthenticatedUser(ctx, t)
	session := r.createOwnedGitHubReviewSession(ctx, t, owner.ID, repoFullName, prNumber)
	createSandboxWithToken(ctx, t, r, session.ID, "sandbox-bearer-token")
	return session
}

func TestPostReviewVerdict_NotFound(t *testing.T) {
	rig := newTestRig(t)

	status, _ := postReviewVerdict(t, rig, "00000000-0000-0000-0000-000000000000", "any-token", "1", validVerdictRequestJSON())
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

func TestPostReviewVerdict_MissingBearer_Unauthorized(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := setupReviewSessionWithSandbox(ctx, t, rig, "acme/verdict-nobearer", 1)

	status, _ := postReviewVerdict(t, rig, session.ID.String(), "", "1", validVerdictRequestJSON())
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

func TestPostReviewVerdict_WrongToken_Unauthorized(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := setupReviewSessionWithSandbox(ctx, t, rig, "acme/verdict-wrongtoken", 2)

	status, _ := postReviewVerdict(t, rig, session.ID.String(), "not-the-real-token", "1", validVerdictRequestJSON())
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

func TestPostReviewVerdict_GenMismatch_Forbidden(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := setupReviewSessionWithSandbox(ctx, t, rig, "acme/verdict-genmismatch", 3)

	status, _ := postReviewVerdict(t, rig, session.ID.String(), "sandbox-bearer-token", "999", validVerdictRequestJSON())
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

func TestPostReviewVerdict_MissingGen_Forbidden(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := setupReviewSessionWithSandbox(ctx, t, rig, "acme/verdict-missinggen", 4)

	status, _ := postReviewVerdict(t, rig, session.ID.String(), "sandbox-bearer-token", "", validVerdictRequestJSON())
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d (a well-formed caller always sends X-Sandbox-Gen)", status, http.StatusForbidden)
	}
}

// TestPostReviewVerdict_DeadSandbox_Gone proves a terminalized sandbox
// (status stopped/failed/stale) can never post a verdict -- mirrors
// scm-credentials/snapshot-mint's own identical dead-sandbox check.
func TestPostReviewVerdict_DeadSandbox_Gone(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := setupReviewSessionWithSandbox(ctx, t, rig, "acme/verdict-dead", 5)

	if _, err := rig.pool.Exec(ctx, `UPDATE sandboxes SET status = 'stopped' WHERE session_id = $1`, session.ID); err != nil {
		t.Fatalf("mark sandbox stopped: %v", err)
	}

	status, _ := postReviewVerdict(t, rig, session.ID.String(), "sandbox-bearer-token", "1", validVerdictRequestJSON())
	if status != http.StatusGone {
		t.Errorf("status = %d, want %d", status, http.StatusGone)
	}
}

// TestPostReviewVerdict_NoGitHubPRMapping_BadRequest proves a session with
// a real sandbox but NO github_pr_sessions row (not a review session at
// all) is rejected 400.
func TestPostReviewVerdict_NoGitHubPRMapping_BadRequest(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()

	session, err := rig.sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceWeb})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	status, _ := postReviewVerdict(t, rig, session.ID.String(), "sandbox-bearer-token", "1", validVerdictRequestJSON())
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (no github_pr_sessions row for this session)", status, http.StatusBadRequest)
	}
}

// TestPostReviewVerdict_MalformedPartialPayload covers this Step's own
// named test-coverage item: "the tool rejecting a malformed or partial
// typed payload". Every case below is a DIFFERENT way a caller's typed
// tool call can be wrong -- missing a required field entirely, an
// out-of-enum value, a negative count, and a syntactically-present-but-
// semantically-empty summary (whitespace only) that only reviewpost.
// ValidateVerdictInput itself catches, since the generated DTO's own
// decode-time check only requires length >= 1, not non-blank content.
func TestPostReviewVerdict_MalformedPartialPayload(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty object -- every field missing", body: `{}`},
		{name: "missing riskLevel entirely (partial payload)", body: `{"premise":"ok","blastRadius":[],"filesChanged":1,"testsCoverage":"adequate","docsDrift":"none","proposedShippable":"auto","summary":"x"}`},
		{name: "missing summary entirely (partial payload)", body: `{"riskLevel":"low","premise":"ok","blastRadius":[],"filesChanged":1,"testsCoverage":"adequate","docsDrift":"none","proposedShippable":"auto"}`},
		{name: "garbled riskLevel enum value", body: `{"riskLevel":"extreme","premise":"ok","blastRadius":[],"filesChanged":1,"testsCoverage":"adequate","docsDrift":"none","proposedShippable":"auto","summary":"x"}`},
		{name: "unrecognized blastRadius tag", body: `{"riskLevel":"low","premise":"ok","blastRadius":["frontend"],"filesChanged":1,"testsCoverage":"adequate","docsDrift":"none","proposedShippable":"auto","summary":"x"}`},
		{name: "negative filesChanged", body: `{"riskLevel":"low","premise":"ok","blastRadius":[],"filesChanged":-1,"testsCoverage":"adequate","docsDrift":"none","proposedShippable":"auto","summary":"x"}`},
		{name: "whitespace-only summary (caught by ValidateVerdictInput, not schema decode)", body: `{"riskLevel":"low","premise":"ok","blastRadius":[],"filesChanged":1,"testsCoverage":"adequate","docsDrift":"none","proposedShippable":"auto","summary":"   "}`},
		{name: "malformed JSON", body: `{not json`},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rig := newTestRig(t)
			ctx := context.Background()
			session := setupReviewSessionWithSandbox(ctx, t, rig, fmt.Sprintf("acme/verdict-malformed-%d", i), int32(i+1))

			status, _ := postReviewVerdict(t, rig, session.ID.String(), "sandbox-bearer-token", "1", tc.body)
			if status != http.StatusBadRequest {
				t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
			}

			// No outbox row must ever be enqueued for a rejected payload.
			var count int
			if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE session_id = $1`, session.ID).Scan(&count); err != nil {
				t.Fatalf("count outbox rows: %v", err)
			}
			if count != 0 {
				t.Errorf("outbox row count = %d, want 0 (a rejected payload must never enqueue anything)", count)
			}
		})
	}
}

// TestPostReviewVerdict_Success_EnqueuesGitHubVerdictOutboxRow proves the
// happy path end to end: 201, a real response body with the SERVER-
// computed Shippable/formalReviewEvent/syncedLabel, and exactly one
// ports.NotificationKindGitHubVerdict outbox row shaped as githubapi.
// VerdictPayload with the right owner/repo/PR/risk data.
func TestPostReviewVerdict_Success_EnqueuesGitHubVerdictOutboxRow(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := setupReviewSessionWithSandbox(ctx, t, rig, "acme/verdict-success", 42)

	status, resp := postReviewVerdict(t, rig, session.ID.String(), "sandbox-bearer-token", "1", validVerdictRequestJSON())
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}

	if resp.Shippable != restdtos.PostReviewVerdictResponseShippableAuto {
		t.Errorf("Shippable = %q, want %q (low risk, ok premise, adequate coverage -> auto)", resp.Shippable, restdtos.PostReviewVerdictResponseShippableAuto)
	}
	if resp.FormalReviewEvent != restdtos.PostReviewVerdictResponseFormalReviewEventCOMMENT {
		t.Errorf("FormalReviewEvent = %q, want %q", resp.FormalReviewEvent, restdtos.PostReviewVerdictResponseFormalReviewEventCOMMENT)
	}
	if resp.SyncedLabel != reviewpost.LabelLowRisk {
		t.Errorf("SyncedLabel = %q, want %q", resp.SyncedLabel, reviewpost.LabelLowRisk)
	}

	var row sqlcgen.Outbox
	if err := rig.pool.QueryRow(ctx, `SELECT id, session_id, kind, payload, status FROM outbox WHERE session_id = $1`, session.ID).
		Scan(&row.ID, &row.SessionID, &row.Kind, &row.Payload, &row.Status); err != nil {
		t.Fatalf("query outbox row: %v", err)
	}
	if row.Kind != string(ports.NotificationKindGitHubVerdict) {
		t.Errorf("Kind = %q, want %q", row.Kind, ports.NotificationKindGitHubVerdict)
	}

	var payload githubapi.VerdictPayload
	if err := json.Unmarshal(row.Payload, &payload); err != nil {
		t.Fatalf("unmarshal outbox payload: %v", err)
	}
	if payload.Owner != "acme" || payload.Repo != "verdict-success" {
		t.Errorf("Owner/Repo = %q/%q, want %q/%q", payload.Owner, payload.Repo, "acme", "verdict-success")
	}
	if payload.PRNumber != 42 {
		t.Errorf("PRNumber = %d, want 42", payload.PRNumber)
	}
	if payload.Event != string(reviewpost.FormalReviewEventComment) {
		t.Errorf("Event = %q, want %q", payload.Event, reviewpost.FormalReviewEventComment)
	}
	if payload.RiskLevel != string(review.RiskLevelLow) {
		t.Errorf("RiskLevel = %q, want %q", payload.RiskLevel, review.RiskLevelLow)
	}
	if !strings.Contains(payload.Body, "Looks good overall, one minor nit below.") {
		t.Errorf("Body = %q, want it to contain the request's own summary", payload.Body)
	}
	if !strings.Contains(payload.Body, "narvi-test-bot") {
		t.Errorf("Body = %q, want it to contain the rendered re-run guidance's bot handle", payload.Body)
	}
}

// TestPostReviewVerdict_ShippableNeverTrustsProposedShippable proves the
// central security property end to end through the real HTTP surface:
// the model's own ProposedShippable ("auto") never overrides the
// server-computed Shippable, which here must be "block" (a not_a_pr
// premise is a hard floor breach regardless of risk/coverage).
func TestPostReviewVerdict_ShippableNeverTrustsProposedShippable(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := setupReviewSessionWithSandbox(ctx, t, rig, "acme/verdict-not-a-pr", 9)

	body := `{
		"riskLevel": "low",
		"premise": "not_a_pr",
		"blastRadius": [],
		"filesChanged": 0,
		"testsCoverage": "skipped",
		"docsDrift": "skipped",
		"proposedShippable": "auto",
		"summary": "This diff is empty; nothing to review."
	}`

	status, resp := postReviewVerdict(t, rig, session.ID.String(), "sandbox-bearer-token", "1", body)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}
	if resp.Shippable != restdtos.PostReviewVerdictResponseShippableBlock {
		t.Errorf("Shippable = %q, want %q (the model's own ProposedShippableAuto must never override the premise floor)", resp.Shippable, restdtos.PostReviewVerdictResponseShippableBlock)
	}
	if resp.FormalReviewEvent != restdtos.PostReviewVerdictResponseFormalReviewEventREQUESTCHANGES {
		t.Errorf("FormalReviewEvent = %q, want %q", resp.FormalReviewEvent, restdtos.PostReviewVerdictResponseFormalReviewEventREQUESTCHANGES)
	}
}

// TestPostReviewVerdict_BlockOnHighRisk covers this Step's own named
// test-coverage item: "blockOnHighRisk both on and off". Same verdict
// input both times (every floor clean, but RiskLevel high -- which alone
// already raises the BASELINE Shippable to needs_human, review.
// baselineFromRisk's own documented behavior; RiskLevel is not one of the
// two named raise-only floors, see review/doc.go's design call #2) --
// only the repo's own repo_settings.block_on_high_risk flag differs, and
// only the resulting FormalReviewEvent (never Shippable itself, which
// stays "needs_human" either way -- blockOnHighRisk changes ONLY which
// GitHub review event this call submits, never the authoritative
// classification) should differ.
func TestPostReviewVerdict_BlockOnHighRisk(t *testing.T) {
	highRiskBody := `{
		"riskLevel": "high",
		"premise": "ok",
		"blastRadius": [],
		"filesChanged": 5,
		"testsCoverage": "adequate",
		"docsDrift": "none",
		"proposedShippable": "auto",
		"summary": "High risk per my own assessment, but every floor is clean."
	}`

	t.Run("off (default, no repo_settings row)", func(t *testing.T) {
		rig := newTestRig(t)
		ctx := context.Background()
		session := setupReviewSessionWithSandbox(ctx, t, rig, "acme/verdict-blockonhighrisk-off", 10)

		status, resp := postReviewVerdict(t, rig, session.ID.String(), "sandbox-bearer-token", "1", highRiskBody)
		if status != http.StatusCreated {
			t.Fatalf("status = %d, want %d", status, http.StatusCreated)
		}
		if resp.Shippable != restdtos.PostReviewVerdictResponseShippableNeedsHuman {
			t.Errorf("Shippable = %q, want %q (blockOnHighRisk never changes Shippable itself)", resp.Shippable, restdtos.PostReviewVerdictResponseShippableNeedsHuman)
		}
		if resp.FormalReviewEvent != restdtos.PostReviewVerdictResponseFormalReviewEventCOMMENT {
			t.Errorf("FormalReviewEvent = %q, want %q (blockOnHighRisk is off)", resp.FormalReviewEvent, restdtos.PostReviewVerdictResponseFormalReviewEventCOMMENT)
		}
	})

	t.Run("on (repo_settings.block_on_high_risk = true)", func(t *testing.T) {
		rig := newTestRig(t)
		ctx := context.Background()
		repoFullName := "acme/verdict-blockonhighrisk-on"
		session := setupReviewSessionWithSandbox(ctx, t, rig, repoFullName, 11)

		if _, err := rig.repoSettings.Upsert(ctx, repoFullName, true, false); err != nil {
			t.Fatalf("upsert repo settings: %v", err)
		}

		status, resp := postReviewVerdict(t, rig, session.ID.String(), "sandbox-bearer-token", "1", highRiskBody)
		if status != http.StatusCreated {
			t.Fatalf("status = %d, want %d", status, http.StatusCreated)
		}
		if resp.Shippable != restdtos.PostReviewVerdictResponseShippableNeedsHuman {
			t.Errorf("Shippable = %q, want %q (blockOnHighRisk never changes Shippable itself)", resp.Shippable, restdtos.PostReviewVerdictResponseShippableNeedsHuman)
		}
		if resp.FormalReviewEvent != restdtos.PostReviewVerdictResponseFormalReviewEventREQUESTCHANGES {
			t.Errorf("FormalReviewEvent = %q, want %q (blockOnHighRisk is on and risk is high)", resp.FormalReviewEvent, restdtos.PostReviewVerdictResponseFormalReviewEventREQUESTCHANGES)
		}
	})
}

// TestPostReviewVerdict_ConcurrentCalls_AllSucceedNoDeadlock is this
// endpoint's own real-concurrency proof, mirroring reviewretrigger.go's
// own identical precedent -- N concurrent verdict-posting-tool calls
// against the SAME review session all succeed, enqueuing exactly N
// outbox rows.
func TestPostReviewVerdict_ConcurrentCalls_AllSucceedNoDeadlock(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := setupReviewSessionWithSandbox(ctx, t, rig, "acme/verdict-concurrent", 77)

	const n = 6
	start := make(chan struct{})
	statuses := make([]int, n)

	done := make(chan int, n)
	for i := 0; i < n; i++ {
		idx := i
		go func() {
			<-start
			status, _ := postReviewVerdict(t, rig, session.ID.String(), "sandbox-bearer-token", "1", validVerdictRequestJSON())
			statuses[idx] = status
			done <- idx
		}()
	}
	close(start)
	for i := 0; i < n; i++ {
		<-done
	}

	for i, status := range statuses {
		if status != http.StatusCreated {
			t.Errorf("statuses[%d] = %d, want %d", i, status, http.StatusCreated)
		}
	}

	var count int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE session_id = $1`, session.ID).Scan(&count); err != nil {
		t.Fatalf("count outbox rows: %v", err)
	}
	if count != n {
		t.Errorf("outbox row count = %d, want %d", count, n)
	}
}
