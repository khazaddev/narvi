//go:build integration

// Integration tests for Step 47's ("server-side verdict", §8.2/§5.2/§21.2)
// own verdict-posting tool (reviewverdict.go), against a real Postgres
// instance -- gated behind the "integration" build tag, sharing this
// package's own testRig (httpapi_integration_test.go).
package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/domain/reviewpost"
	"github.com/khazaddev/narvi/internal/domain/reviewtriage"
)

// validVerdictRequestJSON is the happy-path request body every test in
// this file starts from, mutating only the ONE field a given test cares
// about -- RiskLevel low, Premise ok, empty BlastRadius (legal), adequate
// coverage, no docs drift, the model proposing "auto" (irrelevant to what
// the server actually computes), a real, non-blank Summary, and (§26.2,
// Step 67) an "ok" descriptionAdequacy with its own required explanation.
func validVerdictRequestJSON() string {
	return `{
		"riskLevel": "low",
		"premise": "ok",
		"blastRadius": [],
		"filesChanged": 3,
		"testsCoverage": "adequate",
		"docsDrift": "none",
		"proposedShippable": "auto",
		"summary": "Looks good overall, one minor nit below.",
		"digest": {
			"summary": "Adds a retry helper around the flaky upstream call and swaps every existing call site onto it.",
			"descriptionAdequacy": "ok",
			"adequacyExplanation": "The PR body accurately describes the retry helper this diff adds."
		},
		"factCheck": "done",
		"factCheckKilled": 0
	}`
}

// driftFiringVerdictRequestJSON is validVerdictRequestJSON with filesChanged
// overridden to 25 -- deliberately chosen, paired with a seeded
// server-computed count of 15 (TestPostReviewVerdict_
// FilesChangedDriftCanary_FiresOnDivergence_NeverAffectsVerdict), so the
// fired/not-fired OUTCOME actually depends on which of the two values
// FilesChangedDrifted's own two parameters receive: delta is 10 either
// way (|25-15| == |15-25|), but the RATIO does not, since it divides by
// whichever value lands in the "serverComputed" position -- 10/15 ≈ 0.67
// (>= FilesChangedDriftRatioThreshold, fires) using the CORRECT
// (self-reported, server-computed) order, versus 10/25 = 0.4 (does NOT
// fire) were the two ever accidentally swapped at the call site. A
// same-magnitude pair like 3-vs-50 would fire under EITHER order (both
// ratios land far past the threshold), silently passing a swapped-
// argument regression -- this pair is chosen specifically so it would not.
func driftFiringVerdictRequestJSON() string {
	return strings.Replace(validVerdictRequestJSON(), `"filesChanged": 3`, `"filesChanged": 25`, 1)
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
		// Step 66 (§26.1): "digest" is REQUIRED, and "digest.summary" is
		// the one field within it this Step actually validates.
		{name: "missing digest entirely (Step 66, partial payload)", body: `{"riskLevel":"low","premise":"ok","blastRadius":[],"filesChanged":1,"testsCoverage":"adequate","docsDrift":"none","proposedShippable":"auto","summary":"x"}`},
		{name: "digest present but missing digest.summary entirely (Step 66, partial payload)", body: `{"riskLevel":"low","premise":"ok","blastRadius":[],"filesChanged":1,"testsCoverage":"adequate","docsDrift":"none","proposedShippable":"auto","summary":"x","digest":{}}`},
		{name: "whitespace-only digest.summary (caught by ValidateVerdictInput, not schema decode)", body: `{"riskLevel":"low","premise":"ok","blastRadius":[],"filesChanged":1,"testsCoverage":"adequate","docsDrift":"none","proposedShippable":"auto","summary":"x","digest":{"summary":"   ","descriptionAdequacy":"ok","adequacyExplanation":"x"}}`},
		// §26.2/Step 67: "descriptionAdequacy"/"adequacyExplanation" are
		// REQUIRED on every review from this Step on, the SAME treatment
		// as digest.summary above.
		{name: "digest present but missing digest.descriptionAdequacy entirely (Step 67, partial payload)", body: `{"riskLevel":"low","premise":"ok","blastRadius":[],"filesChanged":1,"testsCoverage":"adequate","docsDrift":"none","proposedShippable":"auto","summary":"x","digest":{"summary":"x","adequacyExplanation":"x"}}`},
		{name: "garbled digest.descriptionAdequacy enum value", body: `{"riskLevel":"low","premise":"ok","blastRadius":[],"filesChanged":1,"testsCoverage":"adequate","docsDrift":"none","proposedShippable":"auto","summary":"x","digest":{"summary":"x","descriptionAdequacy":"somewhat","adequacyExplanation":"x"}}`},
		{name: "digest present but missing digest.adequacyExplanation entirely (Step 67, partial payload)", body: `{"riskLevel":"low","premise":"ok","blastRadius":[],"filesChanged":1,"testsCoverage":"adequate","docsDrift":"none","proposedShippable":"auto","summary":"x","digest":{"summary":"x","descriptionAdequacy":"ok"}}`},
		{name: "whitespace-only digest.adequacyExplanation (caught by ValidateVerdictInput, not schema decode)", body: `{"riskLevel":"low","premise":"ok","blastRadius":[],"filesChanged":1,"testsCoverage":"adequate","docsDrift":"none","proposedShippable":"auto","summary":"x","digest":{"summary":"x","descriptionAdequacy":"ok","adequacyExplanation":"   "}}`},
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

// TestPostReviewVerdict_PersistsReviewVerdictRow_WhenReviewHeadSHAKnown
// is Step 62's own (§21.1, updated for §62 review finding C2) end-to-end
// persistence test: when the session's own CURRENTLY-PROCESSING turn
// carries a review_head_sha (set here exactly the way turn-creation
// itself sets it in production -- turns.Create's own ReviewHeadSha
// param, internal/adapters/inbound/httpapi's createTurnLocked/
// CreateSessionOnTx -- never fabricated some other way), the verdict
// POST appends a review_verdicts row forwarding EVERY field verbatim --
// repo/PR identity, head_sha, and the full Verdict, including the
// server-computed (never client-trusted) Shippable.
func TestPostReviewVerdict_PersistsReviewVerdictRow_WhenReviewHeadSHAKnown(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := setupReviewSessionWithSandbox(ctx, t, rig, "acme/verdict-persist", 55)

	// §62 review finding C2: the head sha now lives on the session's own
	// CURRENTLY-PROCESSING turn (turns.review_head_sha), resolved via
	// TurnStore.GetProcessingTurnForSession -- mirrors how a real review
	// turn is dispatched (status='processing') by the time its own agent
	// calls this endpoint.
	reviewHeadSHA := "sha-persist-abc123"
	if _, err := rig.turns.Create(ctx, sqlcgen.CreateTurnParams{SessionID: session.ID, Status: sqlcgen.TurnStatusProcessing, ReviewHeadSha: &reviewHeadSHA}); err != nil {
		t.Fatalf("seed processing turn with review head sha: %v", err)
	}

	status, _ := postReviewVerdict(t, rig, session.ID.String(), "sandbox-bearer-token", "1", validVerdictRequestJSON())
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}

	var row sqlcgen.ReviewVerdict
	if err := rig.pool.QueryRow(ctx, `SELECT repo_full_name, pr_number, head_sha, risk_level, premise, files_changed, tests_coverage, docs_drift, shippable FROM review_verdicts WHERE repo_full_name = $1 AND pr_number = $2`, "acme/verdict-persist", 55).
		Scan(&row.RepoFullName, &row.PrNumber, &row.HeadSha, &row.RiskLevel, &row.Premise, &row.FilesChanged, &row.TestsCoverage, &row.DocsDrift, &row.Shippable); err != nil {
		t.Fatalf("query review_verdicts row: %v", err)
	}
	if row.HeadSha != "sha-persist-abc123" {
		t.Errorf("head_sha = %q, want %q (forwarded verbatim from pending_head_sha)", row.HeadSha, "sha-persist-abc123")
	}
	if row.RiskLevel != string(review.RiskLevelLow) {
		t.Errorf("risk_level = %q, want %q", row.RiskLevel, review.RiskLevelLow)
	}
	if row.Premise != string(review.PremiseStateOK) {
		t.Errorf("premise = %q, want %q", row.Premise, review.PremiseStateOK)
	}
	if row.FilesChanged != 3 {
		t.Errorf("files_changed = %d, want 3", row.FilesChanged)
	}
	if row.TestsCoverage != string(review.TestsCoverageStateAdequate) {
		t.Errorf("tests_coverage = %q, want %q", row.TestsCoverage, review.TestsCoverageStateAdequate)
	}
	if row.DocsDrift != string(review.DocsDriftStateNone) {
		t.Errorf("docs_drift = %q, want %q", row.DocsDrift, review.DocsDriftStateNone)
	}
	if row.Shippable != string(review.ShippableAuto) {
		t.Errorf("shippable = %q, want %q (server-computed from risk=low/premise=ok/coverage=adequate)", row.Shippable, review.ShippableAuto)
	}
}

// TestPostReviewVerdict_PersistsDigestColumns is Step 66's own (§26.1)
// persistence proof: digest_summary/digest_arch_decisions/
// digest_stack_risks/digest_unverified_limits (migrations/
// 000077_review_verdicts_digest.up.sql) are all populated from the
// posted digest, verbatim, on the SAME review_verdicts row Step 62
// already writes -- "digest quality measurable from day one". It ALSO
// closes the one end-to-end gap on this Step's actual deliverable -- the
// rendered merge readout -- by asserting the SAME posted digest reaches
// the enqueued outbox row's own Body (reviewpost.RenderVerdictComment's
// output, githubapi.VerdictNotifier's own delivery payload): persistence
// and rendering were previously only ever proven in isolation from each
// other (this test proving persistence, TestRenderVerdictComment* proving
// rendering from a hand-built Digest), so a regression that silently
// stopped the digest from ever reaching the POSTED COMMENT -- e.g.
// PostReviewVerdict building the outbox payload from the wrong Digest, or
// dropping it before RenderVerdictComment ever saw it -- would have been
// caught by NEITHER.
func TestPostReviewVerdict_PersistsDigestColumns(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := setupReviewSessionWithSandbox(ctx, t, rig, "acme/verdict-digest-persist", 66)

	reviewHeadSHA := "sha-digest-persist-abc123"
	if _, err := rig.turns.Create(ctx, sqlcgen.CreateTurnParams{SessionID: session.ID, Status: sqlcgen.TurnStatusProcessing, ReviewHeadSha: &reviewHeadSHA}); err != nil {
		t.Fatalf("seed processing turn with review head sha: %v", err)
	}

	body := `{
		"riskLevel": "low",
		"premise": "ok",
		"blastRadius": [],
		"filesChanged": 2,
		"testsCoverage": "adequate",
		"docsDrift": "none",
		"proposedShippable": "auto",
		"summary": "Looks good.",
		"digest": {
			"summary": "Adds a retry helper around the flaky upstream call.",
			"archDecisions": [
				{"decision": "Centralize retries in one helper.", "rejectedAlternative": "Inline retry logic per call site.", "conventionConformance": "Matches CLAUDE.md's shared-helper convention."}
			],
			"stackRisks": "Touches every call site of the upstream client; a regression here is broad.",
			"unverifiedLimits": "Did not verify behavior under a real network partition.",
			"descriptionAdequacy": "drift",
			"adequacyExplanation": "The PR body doesn't mention the new retry helper at all."
		},
		"factCheck": "done",
		"factCheckKilled": 0
	}`

	status, _ := postReviewVerdict(t, rig, session.ID.String(), "sandbox-bearer-token", "1", body)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}

	var digestSummary, digestStackRisks, digestUnverifiedLimits, digestDescriptionAdequacy, digestAdequacyExplanation *string
	var digestArchDecisions []byte
	if err := rig.pool.QueryRow(ctx, `SELECT digest_summary, digest_arch_decisions, digest_stack_risks, digest_unverified_limits, digest_description_adequacy, digest_adequacy_explanation FROM review_verdicts WHERE repo_full_name = $1 AND pr_number = $2`, "acme/verdict-digest-persist", 66).
		Scan(&digestSummary, &digestArchDecisions, &digestStackRisks, &digestUnverifiedLimits, &digestDescriptionAdequacy, &digestAdequacyExplanation); err != nil {
		t.Fatalf("query review_verdicts digest columns: %v", err)
	}

	if digestSummary == nil || *digestSummary != "Adds a retry helper around the flaky upstream call." {
		t.Errorf("digest_summary = %v, want %q", digestSummary, "Adds a retry helper around the flaky upstream call.")
	}
	if digestStackRisks == nil || *digestStackRisks != "Touches every call site of the upstream client; a regression here is broad." {
		t.Errorf("digest_stack_risks = %v, want the posted stackRisks text", digestStackRisks)
	}
	if digestUnverifiedLimits == nil || *digestUnverifiedLimits != "Did not verify behavior under a real network partition." {
		t.Errorf("digest_unverified_limits = %v, want the posted unverifiedLimits text", digestUnverifiedLimits)
	}
	if digestDescriptionAdequacy == nil || *digestDescriptionAdequacy != "drift" {
		t.Errorf("digest_description_adequacy = %v, want %q", digestDescriptionAdequacy, "drift")
	}
	if digestAdequacyExplanation == nil || *digestAdequacyExplanation != "The PR body doesn't mention the new retry helper at all." {
		t.Errorf("digest_adequacy_explanation = %v, want the posted adequacyExplanation text", digestAdequacyExplanation)
	}
	if !strings.Contains(string(digestArchDecisions), "Centralize retries in one helper.") {
		t.Errorf("digest_arch_decisions = %s, want it to contain the posted decision text", digestArchDecisions)
	}
	if !strings.Contains(string(digestArchDecisions), "Matches CLAUDE.md's shared-helper convention.") {
		t.Errorf("digest_arch_decisions = %s, want it to contain the posted conventionConformance text", digestArchDecisions)
	}

	// The end-to-end proof: the SAME digest that just landed in
	// review_verdicts above must ALSO be present in the outbox row's own
	// rendered comment body -- the text that actually gets posted to the
	// PR. Queried the same way TestPostReviewVerdict_
	// Success_EnqueuesGitHubVerdictOutboxRow already does -- scoped to
	// kind = github_verdict specifically (Step 67 own no
	// descriptionAutofix candidate this request has no proposedBody, so
	// only this ONE outbox row is ever enqueued for this session; the
	// explicit kind filter keeps this query correct regardless).
	var row sqlcgen.Outbox
	if err := rig.pool.QueryRow(ctx, `SELECT id, session_id, kind, payload, status FROM outbox WHERE session_id = $1 AND kind = $2`, session.ID, string(ports.NotificationKindGitHubVerdict)).
		Scan(&row.ID, &row.SessionID, &row.Kind, &row.Payload, &row.Status); err != nil {
		t.Fatalf("query outbox row: %v", err)
	}

	var payload githubapi.VerdictPayload
	if err := json.Unmarshal(row.Payload, &payload); err != nil {
		t.Fatalf("unmarshal outbox payload: %v", err)
	}

	for _, want := range []string{
		"### What this PR does",
		"Adds a retry helper around the flaky upstream call.",
		"### Architecture choices",
		"Centralize retries in one helper.",
		"Inline retry logic per call site.",
		"Matches CLAUDE.md's shared-helper convention.",
		"### Risks to the stack",
		"Touches every call site of the upstream client; a regression here is broad.",
		"Did not verify behavior under a real network partition.",
		"Description adequacy",
		"drift",
		"The PR body doesn't mention the new retry helper at all.",
	} {
		if !strings.Contains(payload.Body, want) {
			t.Errorf("outbox payload Body missing %q -- the digest persisted to review_verdicts did not reach the rendered comment. Body:\n%s", want, payload.Body)
		}
	}
}

// TestPostReviewVerdict_SkipsReviewVerdictInsert_WhenNoReviewHeadSHA
// proves a PR whose own processing turn carries NO review_head_sha (e.g.
// a review turn whose own context fetch degraded to no head sha at all,
// reviewcontext.Fetch's own doc comment) still posts successfully --
// review_verdicts persistence is best-effort enrichment, never a
// precondition for this tool call to succeed (see reviewverdict.go's own
// doc comment on this exact point). A real processing turn IS seeded
// here (updated for §62 review finding C2 -- a verdict POST in
// production always corresponds to some real, currently-processing
// turn), just with review_head_sha left nil.
func TestPostReviewVerdict_SkipsReviewVerdictInsert_WhenNoReviewHeadSHA(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := setupReviewSessionWithSandbox(ctx, t, rig, "acme/verdict-no-head-sha", 56)
	// Deliberately no ReviewHeadSha -- this is the fact under test.
	if _, err := rig.turns.Create(ctx, sqlcgen.CreateTurnParams{SessionID: session.ID, Status: sqlcgen.TurnStatusProcessing}); err != nil {
		t.Fatalf("seed processing turn with no review head sha: %v", err)
	}

	status, _ := postReviewVerdict(t, rig, session.ID.String(), "sandbox-bearer-token", "1", validVerdictRequestJSON())
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d (a missing head sha must never fail the verdict post itself)", status, http.StatusCreated)
	}

	var count int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM review_verdicts WHERE repo_full_name = $1 AND pr_number = $2`, "acme/verdict-no-head-sha", 56).Scan(&count); err != nil {
		t.Fatalf("count review_verdicts rows: %v", err)
	}
	if count != 0 {
		t.Errorf("review_verdicts row count = %d, want 0 (no head sha on record, insert must be skipped, never inserted with a fabricated/empty value)", count)
	}
}

// TestPostReviewVerdict_SkipsReviewVerdictInsert_WhenNoProcessingTurnAtAll
// is TestPostReviewVerdict_SkipsReviewVerdictInsert_WhenNoReviewHeadSHA's
// own sibling for the OTHER degraded case §62 review finding C2's fix
// must also handle gracefully: no processing turn can be resolved for
// this session AT ALL (a genuine race -- the turn already completed/
// failed/was cancelled between the agent's own HTTP call landing and
// this handler's own GetProcessingTurnForSession read) -- still posts
// successfully, review_verdicts insert skipped, exactly like the
// no-head-sha case.
func TestPostReviewVerdict_SkipsReviewVerdictInsert_WhenNoProcessingTurnAtAll(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := setupReviewSessionWithSandbox(ctx, t, rig, "acme/verdict-no-processing-turn", 57)
	// Deliberately no turn created at all.

	status, _ := postReviewVerdict(t, rig, session.ID.String(), "sandbox-bearer-token", "1", validVerdictRequestJSON())
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d (no resolvable processing turn must never fail the verdict post itself)", status, http.StatusCreated)
	}

	var count int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM review_verdicts WHERE repo_full_name = $1 AND pr_number = $2`, "acme/verdict-no-processing-turn", 57).Scan(&count); err != nil {
		t.Fatalf("count review_verdicts rows: %v", err)
	}
	if count != 0 {
		t.Errorf("review_verdicts row count = %d, want 0", count)
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
		"summary": "This diff is empty; nothing to review.",
		"digest": {
			"summary": "This PR's diff is empty; there is nothing to describe.",
			"descriptionAdequacy": "ok",
			"adequacyExplanation": "Nothing to compare against; the diff itself is empty."
		},
		"factCheck": "done",
		"factCheckKilled": 0
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
		"summary": "High risk per my own assessment, but every floor is clean.",
		"digest": {
			"summary": "Rewrites the token-refresh path to retry on transient failures.",
			"descriptionAdequacy": "ok",
			"adequacyExplanation": "The PR body accurately describes the token-refresh rewrite."
		},
		"factCheck": "done",
		"factCheckKilled": 0
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

// TestPostReviewVerdict_MisleadingAdequacyRaisesShippable is this Step's
// own end-to-end pin, through the real HTTP surface: an otherwise-clean
// verdict (low risk, ok premise, adequate coverage -- Shippable would
// otherwise compute auto) whose digest.descriptionAdequacy is
// "misleading" must come back as needs_human, and the rendered outbox
// comment must carry the tri-state and its explanation.
func TestPostReviewVerdict_MisleadingAdequacyRaisesShippable(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := setupReviewSessionWithSandbox(ctx, t, rig, "acme/verdict-misleading-adequacy", 12)

	body := `{
		"riskLevel": "low",
		"premise": "ok",
		"blastRadius": [],
		"filesChanged": 1,
		"testsCoverage": "adequate",
		"docsDrift": "none",
		"proposedShippable": "auto",
		"summary": "Small change, but the description doesn't match the diff.",
		"digest": {
			"summary": "Rewrites the auth token refresh path to retry on transient network failures.",
			"descriptionAdequacy": "misleading",
			"adequacyExplanation": "The PR title/body claim this is a docs-only change; the diff rewrites the auth token refresh path."
		},
		"factCheck": "done",
		"factCheckKilled": 0
	}`

	status, resp := postReviewVerdict(t, rig, session.ID.String(), "sandbox-bearer-token", "1", body)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}
	if resp.Shippable != restdtos.PostReviewVerdictResponseShippableNeedsHuman {
		t.Errorf("Shippable = %q, want %q (a misleading description must raise Shippable off an otherwise-clean auto baseline)", resp.Shippable, restdtos.PostReviewVerdictResponseShippableNeedsHuman)
	}

	var row sqlcgen.Outbox
	if err := rig.pool.QueryRow(ctx, `SELECT id, session_id, kind, payload, status FROM outbox WHERE session_id = $1 AND kind = $2`, session.ID, string(ports.NotificationKindGitHubVerdict)).
		Scan(&row.ID, &row.SessionID, &row.Kind, &row.Payload, &row.Status); err != nil {
		t.Fatalf("query outbox row: %v", err)
	}
	var payload githubapi.VerdictPayload
	if err := json.Unmarshal(row.Payload, &payload); err != nil {
		t.Fatalf("unmarshal outbox payload: %v", err)
	}
	if !strings.Contains(payload.Body, "misleading") {
		t.Errorf("outbox payload Body missing the %q tri-state value, Body:\n%s", "misleading", payload.Body)
	}
	if !strings.Contains(payload.Body, "The PR title/body claim this is a docs-only change") {
		t.Errorf("outbox payload Body missing the posted adequacyExplanation, Body:\n%s", payload.Body)
	}
}

// TestPostReviewVerdict_AdequacyNeverAffectsRiskLevel is this Step's own
// end-to-end pin of §26.2's explicit asymmetry: RiskLevel is what the
// caller posted, completely untouched by descriptionAdequacy -- through
// the real HTTP surface, via the SAME risk_level column
// TestPostReviewVerdict_PersistsReviewVerdictRow_WhenReviewHeadSHAKnown
// already asserts, this time with an adequacy value that DOES raise
// Shippable (misleading) alongside a risk the model self-reported as
// medium.
func TestPostReviewVerdict_AdequacyNeverAffectsRiskLevel(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := setupReviewSessionWithSandbox(ctx, t, rig, "acme/verdict-adequacy-risklevel", 13)

	reviewHeadSHA := "sha-adequacy-risklevel-abc123"
	if _, err := rig.turns.Create(ctx, sqlcgen.CreateTurnParams{SessionID: session.ID, Status: sqlcgen.TurnStatusProcessing, ReviewHeadSha: &reviewHeadSHA}); err != nil {
		t.Fatalf("seed processing turn with review head sha: %v", err)
	}

	body := `{
		"riskLevel": "medium",
		"premise": "ok",
		"blastRadius": [],
		"filesChanged": 1,
		"testsCoverage": "adequate",
		"docsDrift": "none",
		"proposedShippable": "auto",
		"summary": "Medium risk per my own assessment.",
		"digest": {
			"summary": "Rewrites the auth token refresh path.",
			"descriptionAdequacy": "misleading",
			"adequacyExplanation": "The PR body claims a docs-only change."
		},
		"factCheck": "done",
		"factCheckKilled": 0
	}`

	status, resp := postReviewVerdict(t, rig, session.ID.String(), "sandbox-bearer-token", "1", body)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}
	// Shippable is raised by the misleading floor (medium risk baseline ->
	// needs_human already, so this alone doesn't distinguish the floor
	// firing -- the persisted risk_level column below is the real proof).
	if resp.Shippable != restdtos.PostReviewVerdictResponseShippableNeedsHuman {
		t.Errorf("Shippable = %q, want %q", resp.Shippable, restdtos.PostReviewVerdictResponseShippableNeedsHuman)
	}

	var riskLevel string
	if err := rig.pool.QueryRow(ctx, `SELECT risk_level FROM review_verdicts WHERE repo_full_name = $1 AND pr_number = $2`, "acme/verdict-adequacy-risklevel", 13).Scan(&riskLevel); err != nil {
		t.Fatalf("query review_verdicts risk_level: %v", err)
	}
	if riskLevel != string(review.RiskLevelMedium) {
		t.Errorf("risk_level = %q, want %q (a misleading descriptionAdequacy must never influence the persisted RiskLevel)", riskLevel, review.RiskLevelMedium)
	}
}

// TestPostReviewVerdict_ProposedBodyPresent_EnqueuesDescriptionAutofixOutboxRow
// proves §26.2's own enqueue-time contract: a posted digest.proposedBody
// enqueues exactly one ports.NotificationKindGitHubDescriptionAutofix
// outbox row, alongside (never instead of) the ordinary
// NotificationKindGitHubVerdict row -- carrying owner/repo/prNumber/
// proposedBody, verbatim. This handler performs NO authorship/flag check
// of its own (DescriptionAutofixPayload's own doc comment) -- that is
// entirely the delivering notifier's own job, at delivery time.
func TestPostReviewVerdict_ProposedBodyPresent_EnqueuesDescriptionAutofixOutboxRow(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := setupReviewSessionWithSandbox(ctx, t, rig, "acme/verdict-autofix-candidate", 14)

	proposedBody := "This PR rewrites the auth token refresh path to retry on transient network failures."
	body := `{
		"riskLevel": "low",
		"premise": "ok",
		"blastRadius": [],
		"filesChanged": 1,
		"testsCoverage": "adequate",
		"docsDrift": "none",
		"proposedShippable": "auto",
		"summary": "Small change, description was stale.",
		"digest": {
			"summary": "Rewrites the auth token refresh path.",
			"descriptionAdequacy": "drift",
			"adequacyExplanation": "The PR body is stale.",
			"proposedBody": "` + proposedBody + `"
		},
		"factCheck": "done",
		"factCheckKilled": 0
	}`

	status, _ := postReviewVerdict(t, rig, session.ID.String(), "sandbox-bearer-token", "1", body)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}

	var count int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE session_id = $1 AND kind = $2`, session.ID, string(ports.NotificationKindGitHubDescriptionAutofix)).Scan(&count); err != nil {
		t.Fatalf("count description-autofix outbox rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("description-autofix outbox row count = %d, want 1", count)
	}

	var payloadBytes []byte
	if err := rig.pool.QueryRow(ctx, `SELECT payload FROM outbox WHERE session_id = $1 AND kind = $2`, session.ID, string(ports.NotificationKindGitHubDescriptionAutofix)).Scan(&payloadBytes); err != nil {
		t.Fatalf("query description-autofix outbox payload: %v", err)
	}
	var payload ports.DescriptionAutofixPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		t.Fatalf("unmarshal description-autofix outbox payload: %v", err)
	}
	if payload.Owner != "acme" || payload.Repo != "verdict-autofix-candidate" {
		t.Errorf("Owner/Repo = %q/%q, want %q/%q", payload.Owner, payload.Repo, "acme", "verdict-autofix-candidate")
	}
	if payload.PRNumber != 14 {
		t.Errorf("PRNumber = %d, want 14", payload.PRNumber)
	}
	if payload.ProposedBody != proposedBody {
		t.Errorf("ProposedBody = %q, want %q", payload.ProposedBody, proposedBody)
	}
	if payload.DescriptionAdequacy != review.DescriptionAdequacyDrift {
		t.Errorf("DescriptionAdequacy = %q, want %q (adversarial-review fix: carried onto the payload verbatim, never re-derived)", payload.DescriptionAdequacy, review.DescriptionAdequacyDrift)
	}

	// The ordinary verdict outbox row must ALSO still be present -- this
	// is an ADDITIVE second row, never a replacement.
	var verdictCount int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE session_id = $1 AND kind = $2`, session.ID, string(ports.NotificationKindGitHubVerdict)).Scan(&verdictCount); err != nil {
		t.Fatalf("count verdict outbox rows: %v", err)
	}
	if verdictCount != 1 {
		t.Errorf("verdict outbox row count = %d, want 1", verdictCount)
	}
}

// TestPostReviewVerdict_AdequacyOKWithProposedBody_NeverEnqueuesDescriptionAutofixOutboxRow
// is the adversarial-review fix's own central regression test (item 2,
// HIGH: "nothing gates the description rewrite on adequacy"): a verdict
// reporting descriptionAdequacy "ok" -- an ACCURATE description -- plus a
// non-blank, unsolicited proposedBody (routine over-filling of an
// optional free-text field, never itself a sign the floor fired) must
// enqueue ZERO description-autofix outbox rows. Before this fix, the
// enqueue's ONLY precondition was "proposedBody non-blank", so this exact
// input would have silently queued a write that collapses an already-
// accurate, human-visible description -- on a verdict that had just
// certified it adequate.
func TestPostReviewVerdict_AdequacyOKWithProposedBody_NeverEnqueuesDescriptionAutofixOutboxRow(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := setupReviewSessionWithSandbox(ctx, t, rig, "acme/verdict-adequacy-ok-candidate", 16)

	body := `{
		"riskLevel": "low",
		"premise": "ok",
		"blastRadius": [],
		"filesChanged": 1,
		"testsCoverage": "adequate",
		"docsDrift": "none",
		"proposedShippable": "auto",
		"summary": "Small change, description is accurate.",
		"digest": {
			"summary": "Rewrites the auth token refresh path.",
			"descriptionAdequacy": "ok",
			"adequacyExplanation": "The PR body already honestly describes the diff.",
			"proposedBody": "An unsolicited stylistic rewrite the agent proposed anyway."
		},
		"factCheck": "done",
		"factCheckKilled": 0
	}`

	status, _ := postReviewVerdict(t, rig, session.ID.String(), "sandbox-bearer-token", "1", body)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}

	var count int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE session_id = $1 AND kind = $2`, session.ID, string(ports.NotificationKindGitHubDescriptionAutofix)).Scan(&count); err != nil {
		t.Fatalf("count description-autofix outbox rows: %v", err)
	}
	if count != 0 {
		t.Errorf("description-autofix outbox row count = %d, want 0 (descriptionAdequacy was \"ok\" -- the floor never fired, so a proposedBody must never be enqueued for a real write)", count)
	}

	// The ordinary verdict outbox row must still be present -- this gate is
	// specific to the description-autofix candidate row, never the verdict
	// posting itself.
	var verdictCount int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE session_id = $1 AND kind = $2`, session.ID, string(ports.NotificationKindGitHubVerdict)).Scan(&verdictCount); err != nil {
		t.Fatalf("count verdict outbox rows: %v", err)
	}
	if verdictCount != 1 {
		t.Errorf("verdict outbox row count = %d, want 1", verdictCount)
	}
}

// TestPostReviewVerdict_ProposedBodyAbsent_NeverEnqueuesDescriptionAutofixOutboxRow
// proves the common case (no rewrite proposed -- validVerdictRequestJSON's
// own digest carries no proposedBody at all) never enqueues the
// description-autofix outbox kind.
func TestPostReviewVerdict_ProposedBodyAbsent_NeverEnqueuesDescriptionAutofixOutboxRow(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := setupReviewSessionWithSandbox(ctx, t, rig, "acme/verdict-no-autofix-candidate", 15)

	status, _ := postReviewVerdict(t, rig, session.ID.String(), "sandbox-bearer-token", "1", validVerdictRequestJSON())
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}

	var count int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE session_id = $1 AND kind = $2`, session.ID, string(ports.NotificationKindGitHubDescriptionAutofix)).Scan(&count); err != nil {
		t.Fatalf("count description-autofix outbox rows: %v", err)
	}
	if count != 0 {
		t.Errorf("description-autofix outbox row count = %d, want 0 (no proposedBody was posted)", count)
	}
}

// seedProcessingTurnWithChangedFilesCount creates a processing turn for
// session carrying reviewHeadSHA (turns.review_head_sha, exactly as
// production turn-creation sets it) and a review_depth_decision JSON blob
// whose own changedFilesCount field is serverComputedChangedFiles --
// mirrors reviewtriage.DecisionRecord's own real wire shape (record.go)
// rather than a hand-typed JSON literal, so a future field rename there
// is caught here at compile time instead of silently producing a JSON
// blob PostReviewVerdict can no longer unmarshal the field it wants out
// of.
func seedProcessingTurnWithChangedFilesCount(ctx context.Context, t *testing.T, r testRig, sessionID pgtype.UUID, reviewHeadSHA string, serverComputedChangedFiles int) {
	t.Helper()
	recordJSON, err := json.Marshal(reviewtriage.DecisionRecord{ChangedFilesCount: serverComputedChangedFiles})
	if err != nil {
		t.Fatalf("marshal review depth decision record: %v", err)
	}
	if _, err := r.turns.Create(ctx, sqlcgen.CreateTurnParams{
		SessionID:           sessionID,
		Status:              sqlcgen.TurnStatusProcessing,
		ReviewHeadSha:       &reviewHeadSHA,
		ReviewDepthDecision: recordJSON,
	}); err != nil {
		t.Fatalf("seed processing turn with review_depth_decision: %v", err)
	}
}

// filesChangedDriftCanaryLogMsg is reviewverdict.go's own exact log
// message for a fired canary -- named here as a literal (this is package
// httpapi_test, a black-box test package) rather than re-deriving it,
// mirroring wantProseFallback's own identical "named literal for an
// unexported production string" precedent elsewhere in this package's
// tests.
const filesChangedDriftCanaryLogMsg = "httpapi: review-verdict: filesChanged drift canary fired -- self-reported and server-computed changed-file counts diverge beyond both thresholds; diagnostic only, verdict unaffected"

// hasLogEntry is findLogEntry's own non-fatal sibling (planapprove_
// integration_test.go) -- reports whether buf contains a line whose "msg"
// equals wantMsg, without failing the test when it does not (the ABSENCE
// of a log line is itself the fact several of this file's own new tests
// below assert).
func hasLogEntry(t *testing.T, buf *bytes.Buffer, wantMsg string) bool {
	t.Helper()
	for _, line := range bytes.Split(buf.Bytes(), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("unmarshal log line %q: %v", line, err)
		}
		if entry["msg"] == wantMsg {
			return true
		}
	}
	return false
}

// TestPostReviewVerdict_FilesChangedDriftCanary_FiresOnDivergence_NeverAffectsVerdict
// is §21.1's own filesChanged drift canary, wired end to end: a processing
// turn's own review_depth_decision records a server-computed
// changedFilesCount (15) against a self-reported filesChanged (25, this
// test's own request body override -- see driftFiringVerdictRequestJSON's
// own doc comment for why 25/15, not e.g. validVerdictRequestJSON's plain
// default, is deliberately chosen) clearing BOTH reviewverdict.
// FilesChangedDriftRatioThreshold and FilesChangedDriftAbsoluteThreshold
// -- the canary must fire a diagnostic log line, and (§21.1's own first
// load-bearing constraint) the posted verdict, the review_verdicts row it
// persists, and this request's own 201 response must all be COMPLETELY
// unaffected: nothing here is a filter or a gate.
func TestPostReviewVerdict_FilesChangedDriftCanary_FiresOnDivergence_NeverAffectsVerdict(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := setupReviewSessionWithSandbox(ctx, t, rig, "acme/verdict-drift-fires", 70)
	seedProcessingTurnWithChangedFilesCount(ctx, t, rig, session.ID, "sha-drift-fires", 15)

	buf := captureDefaultLoggerJSON(t)

	status, resp := postReviewVerdict(t, rig, session.ID.String(), "sandbox-bearer-token", "1", driftFiringVerdictRequestJSON())
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d (a fired canary must never fail the request)", status, http.StatusCreated)
	}
	if resp.Shippable != restdtos.PostReviewVerdictResponseShippable(review.ShippableAuto) {
		t.Errorf("Shippable = %q, want %q (a fired canary must never move the shippable classification)", resp.Shippable, review.ShippableAuto)
	}

	entry := findLogEntry(t, buf, filesChangedDriftCanaryLogMsg)
	if got, want := fmt.Sprintf("%v", entry["self_reported_files_changed"]), "25"; got != want {
		t.Errorf("self_reported_files_changed = %v, want %v", got, want)
	}
	if got, want := fmt.Sprintf("%v", entry["server_computed_files_changed"]), "15"; got != want {
		t.Errorf("server_computed_files_changed = %v, want %v", got, want)
	}

	var filesChanged int
	if err := rig.pool.QueryRow(ctx, `SELECT files_changed FROM review_verdicts WHERE repo_full_name = $1 AND pr_number = $2`, "acme/verdict-drift-fires", 70).Scan(&filesChanged); err != nil {
		t.Fatalf("query review_verdicts row: %v", err)
	}
	if filesChanged != 25 {
		t.Errorf("review_verdicts.files_changed = %d, want 25 (the self-reported value, verbatim -- a fired canary must never rewrite it)", filesChanged)
	}
}

// TestPostReviewVerdict_FilesChangedDriftCanary_NoDivergence_NeverFires is
// the fired test's own negative-space sibling: a server-computed count
// EQUAL to the self-reported one never logs the canary line at all.
func TestPostReviewVerdict_FilesChangedDriftCanary_NoDivergence_NeverFires(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := setupReviewSessionWithSandbox(ctx, t, rig, "acme/verdict-drift-quiet", 71)
	// validVerdictRequestJSON's own filesChanged is 3 -- matching it here
	// exactly so there is nothing to diverge on.
	seedProcessingTurnWithChangedFilesCount(ctx, t, rig, session.ID, "sha-drift-quiet", 3)

	buf := captureDefaultLoggerJSON(t)

	status, _ := postReviewVerdict(t, rig, session.ID.String(), "sandbox-bearer-token", "1", validVerdictRequestJSON())
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}

	if hasLogEntry(t, buf, filesChangedDriftCanaryLogMsg) {
		t.Errorf("filesChanged drift canary fired with no real divergence (self-reported and server-computed both 3); full log:\n%s", buf.String())
	}
}

// TestPostReviewVerdict_FilesChangedDriftCanary_ServerComputedZero_NeverFires
// is §21.1's own second load-bearing constraint, proven end to end: a
// processing turn whose own review_depth_decision was never set at all
// (no ReviewDepthDecision passed to turns.Create, mirroring a turn that
// predates this field, or one whose own marshal step failed at creation
// time) leaves serverComputedChangedFiles at its honest zero value --
// indistinguishable from a genuinely failed GetPullRequest fetch
// (review.PreFetchedContext.ChangedFilesCount's own doc comment) -- and
// the canary must NEVER read that as "confidently zero, so any self-
// report is 100% drift." Deliberately posts a self-reported filesChanged
// of 3 (validVerdictRequestJSON's own default) against a real, wildly
// different server-computed value that would otherwise obviously fire,
// were it not for the zero-guard: this test does not control
// serverComputedChangedFiles at all (no seedProcessingTurnWithChangedFilesCount
// call), which is the point -- it is 0 precisely because it was never
// recorded.
func TestPostReviewVerdict_FilesChangedDriftCanary_ServerComputedZero_NeverFires(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := setupReviewSessionWithSandbox(ctx, t, rig, "acme/verdict-drift-zero-guard", 72)
	reviewHeadSHA := "sha-drift-zero-guard"
	// Deliberately no ReviewDepthDecision -- review_depth_decision stays
	// SQL NULL, exactly like a turn that predates this field.
	if _, err := rig.turns.Create(ctx, sqlcgen.CreateTurnParams{SessionID: session.ID, Status: sqlcgen.TurnStatusProcessing, ReviewHeadSha: &reviewHeadSHA}); err != nil {
		t.Fatalf("seed processing turn with no review_depth_decision: %v", err)
	}

	buf := captureDefaultLoggerJSON(t)

	status, _ := postReviewVerdict(t, rig, session.ID.String(), "sandbox-bearer-token", "1", validVerdictRequestJSON())
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}

	if hasLogEntry(t, buf, filesChangedDriftCanaryLogMsg) {
		t.Errorf("filesChanged drift canary fired against an unset (zero) server-computed count -- must be treated as no reliable signal, never as a real zero; full log:\n%s", buf.String())
	}
}

// seedProcessingTurnWithUndeliveredDiff mirrors
// seedProcessingTurnWithChangedFilesCount, but ALSO sets DiffEmpty/
// DiffTruncated on the seeded review_depth_decision record -- D4's own
// (adversarial review of PR #182, MEDIUM) end-to-end fixture: a
// processing turn whose diff was never fully delivered to the reviewing
// agent, used below to prove FilesChangedDrifted's own diffDelivered
// guard actually suppresses the canary through the FULL httpapi wiring
// (the unmarshal, the diffDelivered computation, and the call site) --
// driftcanary_test.go already covers the pure function in isolation, this
// proves the wiring around it too.
func seedProcessingTurnWithUndeliveredDiff(ctx context.Context, t *testing.T, r testRig, sessionID pgtype.UUID, reviewHeadSHA string, serverComputedChangedFiles int, diffEmpty, diffTruncated bool) {
	t.Helper()
	recordJSON, err := json.Marshal(reviewtriage.DecisionRecord{ChangedFilesCount: serverComputedChangedFiles, DiffEmpty: diffEmpty, DiffTruncated: diffTruncated})
	if err != nil {
		t.Fatalf("marshal review depth decision record: %v", err)
	}
	if _, err := r.turns.Create(ctx, sqlcgen.CreateTurnParams{
		SessionID:           sessionID,
		Status:              sqlcgen.TurnStatusProcessing,
		ReviewHeadSha:       &reviewHeadSHA,
		ReviewDepthDecision: recordJSON,
	}); err != nil {
		t.Fatalf("seed processing turn with undelivered-diff review_depth_decision: %v", err)
	}
}

// TestPostReviewVerdict_FilesChangedDriftCanary_DiffTruncated_NeverFires is
// D4's own (adversarial review of PR #182, MEDIUM) end-to-end fix: the
// EXACT SAME 25-vs-15 divergence as TestPostReviewVerdict_
// FilesChangedDriftCanary_FiresOnDivergence_NeverAffectsVerdict above --
// which clears both thresholds and fires there -- must NOT fire here,
// because this turn's own review_depth_decision also records
// DiffTruncated=true. The reviewing agent was handed only a partial diff
// (with an explicit truncation notice, review.RenderTurnPrompt's own
// doc comment), so its own under-count is the CORRECT, honest
// consequence of what it actually saw -- not evidence of a skipped
// review, and this canary must never blame it for a server-side delivery
// failure that was never its own fault.
func TestPostReviewVerdict_FilesChangedDriftCanary_DiffTruncated_NeverFires(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := setupReviewSessionWithSandbox(ctx, t, rig, "acme/verdict-drift-diff-truncated", 73)
	seedProcessingTurnWithUndeliveredDiff(ctx, t, rig, session.ID, "sha-drift-truncated", 15, false, true)

	buf := captureDefaultLoggerJSON(t)

	status, _ := postReviewVerdict(t, rig, session.ID.String(), "sandbox-bearer-token", "1", driftFiringVerdictRequestJSON())
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}

	if hasLogEntry(t, buf, filesChangedDriftCanaryLogMsg) {
		t.Errorf("filesChanged drift canary fired against a TRUNCATED diff -- the reviewer was never handed the full diff, so its own count divergence is not evidence of a skipped review; full log:\n%s", buf.String())
	}
}

// TestPostReviewVerdict_FilesChangedDriftCanary_DiffEmpty_NeverFires is
// TestPostReviewVerdict_FilesChangedDriftCanary_DiffTruncated_NeverFires's
// own sibling for the OTHER diff-not-delivered case (D4): the diff fetch
// itself never even succeeded (GetCompareDiff failed, review.
// PreFetchedContext.Diff == ""), so review.RenderTurnPrompt rendered no
// diff block at all -- the reviewing agent had nothing to count files
// against, and its own self-report diverging from the server-computed
// count is, again, not evidence of anything this canary should surface.
func TestPostReviewVerdict_FilesChangedDriftCanary_DiffEmpty_NeverFires(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := setupReviewSessionWithSandbox(ctx, t, rig, "acme/verdict-drift-diff-empty", 74)
	seedProcessingTurnWithUndeliveredDiff(ctx, t, rig, session.ID, "sha-drift-empty", 15, true, false)

	buf := captureDefaultLoggerJSON(t)

	status, _ := postReviewVerdict(t, rig, session.ID.String(), "sandbox-bearer-token", "1", driftFiringVerdictRequestJSON())
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}

	if hasLogEntry(t, buf, filesChangedDriftCanaryLogMsg) {
		t.Errorf("filesChanged drift canary fired when the diff was never delivered at all (empty); full log:\n%s", buf.String())
	}
}
