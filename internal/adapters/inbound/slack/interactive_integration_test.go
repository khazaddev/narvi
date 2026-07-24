//go:build integration

// Full HTTP-level integration tests for internal/adapters/inbound/slack's
// POST /webhooks/slack/interactive handler (Step 38, "plan mode,
// cross-channel", §8.1/§13.3), against a real Postgres instance --
// mirrors handler_integration_test.go's own testcontainers convention
// exactly, reusing that file's newTestPool (same package).
package slack_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/inbound/slack"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/adapters/outbound/slackapi"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/platform"
)

// recordedSlackRequest captures one request the fake Slack API server
// observed -- enough for this file's own assertions (which endpoint, what
// body).
type recordedSlackRequest struct {
	path string
	body map[string]any
}

// interactiveTestRig bundles a fully-wired NewInteractivityHandler plus
// the stores needed to seed/assert against Postgres directly, and a fake
// Slack API server recording every request it receives (chat.update/
// views.open).
type interactiveTestRig struct {
	handler  http.HandlerFunc
	pool     *pgxpool.Pool
	sessions *narvipg.SessionStore
	turns    *narvipg.TurnStore
	plans    *narvipg.PlanStore

	requests chan recordedSlackRequest
}

func newInteractiveTestRig(t *testing.T, pool *pgxpool.Pool) *interactiveTestRig {
	t.Helper()
	return newInteractiveTestRigWithTimeouts(t, pool, platform.DefaultTimeouts())
}

// newInteractiveTestRigWithTimeouts is newInteractiveTestRig's own
// parameterized twin, letting a test (e.g. the SlackInteractivityAckTimeout
// regression test below) override just the Timeouts a real handler is wired
// with -- e.g. a small SlackInteractivityAckTimeout so a DB-contention test
// doesn't have to wait out the real production default.
func newInteractiveTestRigWithTimeouts(t *testing.T, pool *pgxpool.Pool, timeouts platform.Timeouts) *interactiveTestRig {
	t.Helper()
	ctx := context.Background()

	requests := make(chan recordedSlackRequest, 16)
	fakeSlack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		requests <- recordedSlackRequest{path: r.URL.Path, body: body}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(fakeSlack.Close)

	sessions := narvipg.NewSessionStore(pool)
	turns := narvipg.NewTurnStore(pool)
	plans := narvipg.NewPlanStore(pool)
	outbox := narvipg.NewOutboxStore(pool)
	linearAgentSessions := narvipg.NewLinearAgentSessionStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)

	registry, err := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "http://localhost:8080", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = registry.Shutdown() })

	slackClient := slackapi.New(fakeSlack.Client(), fakeSlack.URL, "test-bot-token")

	handler := slack.NewInteractivityHandler(slack.InteractiveDeps{
		Pool:                pool,
		Sessions:            sessions,
		Turns:               turns,
		Plans:               plans,
		Outbox:              outbox,
		LinearAgentSessions: linearAgentSessions,
		Registry:            registry,
		SlackClient:         slackClient,
		AuditLog:            auditLog,
		SigningSecret:       testSigningSecret,
		Timeouts:            timeouts,
	})

	return &interactiveTestRig{handler: handler, pool: pool, sessions: sessions, turns: turns, plans: plans, requests: requests}
}

// signedInteractivityRequest builds a real, correctly-signed POST
// .../webhooks/slack/interactive request carrying a form-encoded "payload"
// field -- mirrors signedSlackRequest's own identical HMAC scheme
// (handler_integration_test.go), just over a form body instead of raw
// JSON (this Step's own structurally different payload shape).
func signedInteractivityRequest(t *testing.T, payload string) *http.Request {
	t.Helper()
	body := url.Values{"payload": {payload}}.Encode()

	ts := time.Now().Unix()
	signedPayload := "v0:" + strconv.FormatInt(ts, 10) + ":" + body
	mac := hmac.New(sha256.New, []byte(testSigningSecret))
	mac.Write([]byte(signedPayload))
	sig := "v0=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/webhooks/slack/interactive", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Slack-Request-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-Slack-Signature", sig)
	return req
}

// seedSessionTurnAndAwaitingPlan seeds a plain 'web'-spawn-source session
// (spawn_source is irrelevant to this handler's own behavior -- Slack/
// Linear verdicts are unauthenticated regardless of the session's own
// origin, this Step's own explicit precedent), a Completed plan_mode=true
// producing turn, and an awaiting_approval plan atop it -- mirrors
// httpapi_test's own seedAwaitingApprovalPlan precedent exactly, duplicated
// here since this file lives in a different package.
func seedSessionTurnAndAwaitingPlan(ctx context.Context, t *testing.T, rig *interactiveTestRig) (sqlcgen.Session, sqlcgen.Plan) {
	t.Helper()
	session, err := rig.sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceWeb})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	turn, err := rig.turns.Create(ctx, sqlcgen.CreateTurnParams{
		SessionID: session.ID,
		Status:    sqlcgen.TurnStatusCompleted,
		PlanMode:  true,
	})
	if err != nil {
		t.Fatalf("seed producing turn: %v", err)
	}
	plan, err := rig.plans.Create(ctx, sqlcgen.CreatePlanParams{
		SessionID: session.ID,
		TurnID:    turn.ID,
		Version:   1,
		Status:    sqlcgen.PlanStatusAwaitingApproval,
	})
	if err != nil {
		t.Fatalf("seed awaiting_approval plan: %v", err)
	}
	return session, plan
}

// blockActionsPayloadJSON builds a real-shaped block_actions interaction
// payload JSON string.
func blockActionsPayloadJSON(actionID, value, channel, messageTS, triggerID string) string {
	payload := map[string]any{
		"type":       "block_actions",
		"trigger_id": triggerID,
		"channel":    map[string]string{"id": channel},
		"message":    map[string]string{"ts": messageTS},
		"actions":    []map[string]string{{"action_id": actionID, "value": value}},
	}
	raw, _ := json.Marshal(payload)
	return string(raw)
}

// TestInteractivityHandler_BlockActions_ApprovePlan proves the full,
// real, DB-backed path: a signed approve_plan button click decides the
// plan via the shared httpapi.DecidePlan (the plan flips to 'approved' and
// a new implementation turn is created) AND synchronously updates the
// Slack message via a real chat.update call.
func TestInteractivityHandler_BlockActions_ApprovePlan(t *testing.T) {
	pool := newTestPool(t)
	rig := newInteractiveTestRig(t, pool)
	ctx := context.Background()

	session, plan := seedSessionTurnAndAwaitingPlan(ctx, t, rig)
	value := slackapi.EncodePlanActionValue(plan.ID.String(), session.ID.String())
	payload := blockActionsPayloadJSON(slackapi.ActionApprovePlan, value, "C1", "1700000000.000001", "trigger-1")

	req := signedInteractivityRequest(t, payload)
	rec := httptest.NewRecorder()
	rig.handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body=%s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	var dbStatus sqlcgen.PlanStatus
	var decidedBy pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT status, decided_by FROM plans WHERE id = $1`, plan.ID).Scan(&dbStatus, &decidedBy); err != nil {
		t.Fatalf("query plan row: %v", err)
	}
	if dbStatus != sqlcgen.PlanStatusApproved {
		t.Errorf("db status = %q, want %q", dbStatus, sqlcgen.PlanStatusApproved)
	}
	if decidedBy.Valid {
		t.Errorf("decided_by = %v, want NULL (bot/channel attribution, no per-user gate for Slack)", decidedBy)
	}

	turns, err := rig.turns.ListForSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("len(turns) = %d, want 2 (the seeded producing turn + the new implementation turn)", len(turns))
	}

	select {
	case got := <-rig.requests:
		if got.path != "/chat.update" {
			t.Errorf("recorded request path = %q, want %q", got.path, "/chat.update")
		}
		if got.body["channel"] != "C1" || got.body["ts"] != "1700000000.000001" {
			t.Errorf("chat.update (channel, ts) = (%v, %v), want (%q, %q)", got.body["channel"], got.body["ts"], "C1", "1700000000.000001")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the synchronous chat.update call")
	}
}

// TestInteractivityHandler_BlockActions_RejectPlan is the reject twin --
// no new turn is created, and the message update reflects rejection.
func TestInteractivityHandler_BlockActions_RejectPlan(t *testing.T) {
	pool := newTestPool(t)
	rig := newInteractiveTestRig(t, pool)
	ctx := context.Background()

	session, plan := seedSessionTurnAndAwaitingPlan(ctx, t, rig)
	value := slackapi.EncodePlanActionValue(plan.ID.String(), session.ID.String())
	payload := blockActionsPayloadJSON(slackapi.ActionRejectPlan, value, "C1", "1700000000.000002", "trigger-2")

	req := signedInteractivityRequest(t, payload)
	rec := httptest.NewRecorder()
	rig.handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body=%s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	var dbStatus sqlcgen.PlanStatus
	if err := pool.QueryRow(ctx, `SELECT status FROM plans WHERE id = $1`, plan.ID).Scan(&dbStatus); err != nil {
		t.Fatalf("query plan row: %v", err)
	}
	if dbStatus != sqlcgen.PlanStatusRejected {
		t.Errorf("db status = %q, want %q", dbStatus, sqlcgen.PlanStatusRejected)
	}

	turns, err := rig.turns.ListForSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(turns) != 1 {
		t.Errorf("len(turns) = %d, want 1 (reject never creates a new turn)", len(turns))
	}

	select {
	case got := <-rig.requests:
		if got.path != "/chat.update" {
			t.Errorf("recorded request path = %q, want %q", got.path, "/chat.update")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the synchronous chat.update call")
	}
}

// TestInteractivityHandler_ViewSubmission_CreatesRequestChangesTurn proves
// the modal's own submission creates a real plan_mode=true turn carrying
// the submitted feedback text, via the exact SAME httpapi.CreateTurnCore
// path POST .../turns itself uses.
func TestInteractivityHandler_ViewSubmission_CreatesRequestChangesTurn(t *testing.T) {
	pool := newTestPool(t)
	rig := newInteractiveTestRig(t, pool)
	ctx := context.Background()

	session, plan := seedSessionTurnAndAwaitingPlan(ctx, t, rig)
	privateMetadata := slackapi.EncodePlanActionValue(plan.ID.String(), session.ID.String())

	viewSubmission := map[string]any{
		"type": "view_submission",
		"view": map[string]any{
			"callback_id":      slackapi.RequestChangesCallbackID,
			"private_metadata": privateMetadata,
			"state": map[string]any{
				"values": map[string]any{
					slackapi.RequestChangesBlockID: map[string]any{
						slackapi.RequestChangesActionID: map[string]any{
							"type":  "plain_text_input",
							"value": "please keep the fallback path",
						},
					},
				},
			},
		},
	}
	raw, err := json.Marshal(viewSubmission)
	if err != nil {
		t.Fatalf("marshal view_submission payload: %v", err)
	}

	req := signedInteractivityRequest(t, string(raw))
	rec := httptest.NewRecorder()
	rig.handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body=%s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	turns, err := rig.turns.ListForSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("len(turns) = %d, want 2 (the seeded producing turn + the new request-changes turn)", len(turns))
	}
	var newTurn *sqlcgen.Turn
	for i := range turns {
		if turns[i].ID != plan.TurnID {
			newTurn = &turns[i]
		}
	}
	if newTurn == nil {
		t.Fatal("no new turn found besides the seeded producing turn")
	}
	if !newTurn.PlanMode {
		t.Error("new turn PlanMode = false, want true (a request-changes turn)")
	}
	if newTurn.Prompt == nil || *newTurn.Prompt != "please keep the fallback path" {
		t.Errorf("new turn Prompt = %v, want %q", newTurn.Prompt, "please keep the fallback path")
	}
	if newTurn.Status != sqlcgen.TurnStatusPending {
		t.Errorf("new turn Status = %q, want %q", newTurn.Status, sqlcgen.TurnStatusPending)
	}
}

// TestInteractivityHandler_BlockActions_BoundedBySlackInteractivityAckTimeout
// is the regression test for a confirmed adversarial-review finding: the
// approve_plan/reject_plan block_actions path used to run its ENTIRE
// synchronous decide+update sequence (httpapi.DecidePlan's own guarded
// transaction -- including acquiring the session row's own lock -- THEN the
// chat.update call) against the bare, UNBOUNDED r.Context(), rather than a
// context bounded by any timeout matched to Slack's own real ~3s
// interactivity-ack budget. Under DB contention, this handler could and
// would block for arbitrarily long, well past that real budget, causing
// Slack to show the user a spurious "dispatch_failed" error even though
// Narvi's own backend would have eventually completed the action correctly.
//
// This simulates real DB contention directly: a COMPETING, otherwise-
// unrelated transaction holds the exact row lock DecidePlanOnTx's own first
// step acquires (GetSessionActorEpochForUpdate, session_store.go) on the
// SAME session, for far longer than the rig's own (deliberately small, for
// test speed) SlackInteractivityAckTimeout. It asserts:
//
//   - the handler still returns well before the competing lock is released
//     (proving the decide+update sequence was cut short by the new bounded
//     context, not left blocked until the lock's own holder let go)
//   - the handler still acks Slack with its own unconditional 200 anyway
//     (Slack's own documented contract: ack within the window regardless of
//     whether the underlying work actually finished)
//   - the plan's own status is left untouched (still awaiting_approval),
//     since the guarded UPDATE never got a chance to run before the shared
//     bounded context's deadline hit
//
// Against the PRE-FIX code (a bare r.Context() with no
// SlackInteractivityAckTimeout wrap), this test would instead block for the
// competing lock's FULL hold duration before ever seeing a response --
// failing this test's own elapsed-time assertion below -- proving this is a
// real regression test, not a tautology.
func TestInteractivityHandler_BlockActions_BoundedBySlackInteractivityAckTimeout(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()

	const interactivityTimeout = 500 * time.Millisecond
	const lockHoldDuration = 3 * time.Second

	timeouts := platform.DefaultTimeouts()
	timeouts.WebhookTimestampFreshnessWindow = 5 * time.Minute
	timeouts.SlackInteractivityAckTimeout = interactivityTimeout

	rig := newInteractiveTestRigWithTimeouts(t, pool, timeouts)
	session, plan := seedSessionTurnAndAwaitingPlan(ctx, t, rig)

	// Hold a competing row lock on the SAME session, via the exact query
	// DecidePlanOnTx's own first step acquires -- simulates real DB
	// contention (e.g. a concurrent decision, or any other transactional
	// writer of this session row) without needing to reproduce a genuine
	// race.
	lockTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin competing lock tx: %v", err)
	}
	if _, err := rig.sessions.WithTx(lockTx).GetActorEpochForUpdate(ctx, session.ID); err != nil {
		t.Fatalf("acquire competing session row lock: %v", err)
	}
	releaseTimer := time.AfterFunc(lockHoldDuration, func() {
		_ = lockTx.Rollback(ctx)
	})
	t.Cleanup(func() {
		releaseTimer.Stop()
		_ = lockTx.Rollback(ctx) // no-op if already released above
	})

	value := slackapi.EncodePlanActionValue(plan.ID.String(), session.ID.String())
	payload := blockActionsPayloadJSON(slackapi.ActionApprovePlan, value, "C1", "1700000000.000009", "trigger-9")
	req := signedInteractivityRequest(t, payload)
	rec := httptest.NewRecorder()

	start := time.Now()
	rig.handler(rec, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (a bounded-context timeout must still ack Slack with 200)", rec.Code, http.StatusOK)
	}
	if elapsed >= lockHoldDuration {
		t.Errorf("handler took %s to respond, want well under the %s competing lock hold -- the decide+update "+
			"sequence should have been cut short by SlackInteractivityAckTimeout (%s), not blocked until the "+
			"competing lock was released (proves the fix is actually wired in)", elapsed, lockHoldDuration, interactivityTimeout)
	}

	var dbStatus sqlcgen.PlanStatus
	if err := pool.QueryRow(ctx, `SELECT status FROM plans WHERE id = $1`, plan.ID).Scan(&dbStatus); err != nil {
		t.Fatalf("query plan row: %v", err)
	}
	if dbStatus != sqlcgen.PlanStatusAwaitingApproval {
		t.Errorf("db status = %q, want %q (the guarded update must never have run before the bounded context's deadline)", dbStatus, sqlcgen.PlanStatusAwaitingApproval)
	}
}
