//go:build integration

// Audit-fix batch tests (findings L2/H7/M6/L7/L20) for
// internal/adapters/inbound/linear's own handlePrompted (webhook.go) --
// mirrors webhook_integration_test.go's/identity_integration_test.go's own
// conventions exactly (same package, same newTestPool/postWebhook/
// agentSessionCreatedPayload/agentSessionPromptedPayloadWithUser/
// installLinearFixture helpers).
package linear_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/inbound/linear"
	"github.com/khazaddev/narvi/internal/adapters/outbound/linearapi"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// newGenericLinearGraphQLStub answers EVERY GraphQL request (both the
// "user(id){email}" identity query AND the agentActivityCreate mutation --
// both hit the SAME endpoint) with one fixed response shape carrying
// both possible top-level fields; json.Unmarshal ignores whichever one a
// given caller's own struct doesn't declare, so one stub genuinely covers
// both call shapes. Every raw request body is recorded (mutex-protected --
// this file's own concurrency test fires many requests in parallel) for
// this file's own assertions on what was actually posted back to Linear.
func newGenericLinearGraphQLStub(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"user":{"email":"nobody-matches@example.com"},"agentActivityCreate":{"success":true}}}`))
	}))
	t.Cleanup(server.Close)
	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, len(bodies))
		copy(out, bodies)
		return out
	}
}

// TestWebhookHandler_Prompted_ConcurrentReplies_L2_OnlyOneSucceeds is the
// flagship L2 regression test: BEFORE this batch, handlePrompted's own
// ordinary-reply path called Turns.Create DIRECTLY on the raw pool, with
// NO transaction and NO lock -- a genuine check-then-act race. This fires
// N near-simultaneous `prompted` replies (distinct Linear-Delivery ids, so
// none are deduped away by the webhook-delivery claim -- each represents a
// genuinely distinct incoming AgentActivity) at the SAME already-backed
// session, and proves exactly ONE wins (creates a turn, gets its own
// turn.create audit_log row) while every loser gets the honest busy
// message (M6) -- never a silently dropped duplicate, and never two turns
// racing past the open-turn check.
func TestWebhookHandler_Prompted_ConcurrentReplies_L2_OnlyOneSucceeds(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	deps := newHandlerDeps(t, pool)
	// installLinearFixture below (needed for this test's own GraphQL stub)
	// means decryptLinearAccessToken succeeds, so resolveActor (identity.go)
	// proceeds past its "no installation" short-circuit into
	// identitylink.Resolve -- deps.IdentityLink must be wired up first,
	// exactly like every other Linear test that reaches real identity
	// resolution (see authz_backend_error_integration_test.go).
	deps.IdentityLink = newIdentityLinkDepsForTest(pool, deps.AuditLog)

	organizationID := "org-l2-concurrent-1"
	installLinearFixture(ctx, t, pool, organizationID, deps.TokenEncryptionKey)

	stub, recordedBodies := newGenericLinearGraphQLStub(t)
	deps.LinearClient = linearapi.New(stub.Client(), stub.URL)

	handler := linear.NewWebhookHandler(deps)

	agentSessionID := "agent-session-l2-concurrent-1"
	createdBody := agentSessionCreatedPayload(agentSessionID, organizationID)
	rec := postWebhook(t, handler, createdBody, "delivery-l2-created-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("created status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// Drive the `created` event's own initial turn terminal so the
	// concurrent replies below race against a clean "no open turn"
	// starting state -- mirrors identity_integration_test.go's own
	// identical setup.
	if _, err := pool.Exec(ctx,
		`UPDATE turns SET status = 'completed' WHERE session_id = (SELECT session_id FROM linear_agent_sessions WHERE agent_session_id = $1)`,
		agentSessionID,
	); err != nil {
		t.Fatalf("mark fixture turn completed: %v", err)
	}

	agentSessions := narvipg.NewLinearAgentSessionStore(pool)
	mapping, err := agentSessions.GetByAgentSessionID(ctx, agentSessionID)
	if err != nil {
		t.Fatalf("GetByAgentSessionID: %v", err)
	}
	sessionID := mapping.SessionID

	const n = 8
	statuses := make([]int, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			// A distinct userId per goroutine -- keeps each reply's own
			// identity resolution independent, so this test's own
			// assertions stay about the turn-creation LOCK, never
			// incidental contention inside identitylink's own
			// link-prompt upsert for a SHARED external id.
			body := agentSessionPromptedPayloadWithUser(agentSessionID, organizationID, fmt.Sprintf("linear-l2-user-%d", i), fmt.Sprintf("reply %d", i))
			r := postWebhook(t, handler, body, fmt.Sprintf("delivery-l2-prompted-%d", i))
			statuses[i] = r.Code
		}(i)
	}
	wg.Wait()

	for i, s := range statuses {
		if s != http.StatusOK {
			t.Errorf("reply %d: status = %d, want %d", i, s, http.StatusOK)
		}
	}

	turns := narvipg.NewTurnStore(pool)
	finalTurns, err := turns.ListForSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("ListForSession: %v", err)
	}
	if len(finalTurns) != 2 {
		t.Fatalf("len(turns) = %d, want exactly 2 (the seeded completed turn, plus EXACTLY ONE winning reply turn -- never a duplicate slipping past the lock)", len(finalTurns))
	}

	var replyTurnID pgtype.UUID
	for _, tn := range finalTurns {
		if tn.Status != sqlcgen.TurnStatusCompleted {
			replyTurnID = tn.ID
		}
	}
	if !replyTurnID.Valid {
		t.Fatal("could not identify the winning reply's own new turn")
	}

	var auditCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE action = 'turn.create' AND resource_type = 'turn' AND resource_id = $1`,
		replyTurnID.String(),
	).Scan(&auditCount); err != nil {
		t.Fatalf("count audit_log rows: %v", err)
	}
	if auditCount != 1 {
		t.Errorf("audit_log row count for winning reply turn = %d, want 1 (H7)", auditCount)
	}

	// M6: every LOSER must get the honest busy message -- never a
	// silently dropped duplicate. Exactly n-1 losers.
	var busyCount int
	for _, b := range recordedBodies() {
		if strings.Contains(b, "wasn't queued") {
			busyCount++
		}
	}
	if busyCount != n-1 {
		t.Errorf("busy-message activity count = %d, want exactly %d (one honest reply per loser)", busyCount, n-1)
	}
}

// TestWebhookHandler_Prompted_StopSignal_PostsHonestReply is the L7 audit
// fix's own regression test: BEFORE this batch, a `stop` signal was only
// ever logged and silently discarded, with no reply telling the user
// cancellation isn't supported. This proves an honest reply is now posted
// instead, and that the signal never touches any turn (no real
// cancellation is implemented -- narrow scope, per this batch's own
// brief).
func TestWebhookHandler_Prompted_StopSignal_PostsHonestReply(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	deps := newHandlerDeps(t, pool)

	organizationID := "org-stop-signal-1"
	installLinearFixture(ctx, t, pool, organizationID, deps.TokenEncryptionKey)

	stub, recordedBodies := newGenericLinearGraphQLStub(t)
	deps.LinearClient = linearapi.New(stub.Client(), stub.URL)

	handler := linear.NewWebhookHandler(deps)

	agentSessionID := "agent-session-stop-signal-1"
	createdBody := agentSessionCreatedPayload(agentSessionID, organizationID)
	rec := postWebhook(t, handler, createdBody, "delivery-stop-created-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("created status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	stopBody := []byte(fmt.Sprintf(`{
		"action": "prompted",
		"type": "AgentSessionEvent",
		"organizationId": %q,
		"webhookTimestamp": %d,
		"agentSession": {"id": %q},
		"agentActivity": {"userId": "linear-stop-user-1", "signal": "stop", "content": {"type": "prompt", "body": "stop"}}
	}`, organizationID, time.Now().UnixMilli(), agentSessionID))

	rec2 := postWebhook(t, handler, stopBody, "delivery-stop-prompted-1")
	if rec2.Code != http.StatusOK {
		t.Fatalf("stop status = %d, want %d; body=%s", rec2.Code, http.StatusOK, rec2.Body.String())
	}

	var turnCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM turns WHERE session_id = (SELECT session_id FROM linear_agent_sessions WHERE agent_session_id = $1)`,
		agentSessionID,
	).Scan(&turnCount); err != nil {
		t.Fatalf("count turns: %v", err)
	}
	if turnCount != 1 {
		t.Errorf("turn count = %d, want 1 (only the created event's own initial turn -- a stop signal must never create or otherwise touch a turn)", turnCount)
	}

	var gotHonestReply bool
	for _, b := range recordedBodies() {
		if strings.Contains(b, "isn't supported yet") {
			gotHonestReply = true
		}
	}
	if !gotHonestReply {
		t.Error("no outbound activity contained the L7 honest stop-not-supported reply")
	}
}

// TestWebhookHandler_Prompted_LogsSessionAndTurnID is the L20 audit fix's
// own regression test: this package previously logged session CREATION
// but never a reply turn. Captures slog's own default logger output
// (mirroring setsessionid_retry_integration_test.go's own identical
// precedent) and asserts session_id/turn_id both appear.
func TestWebhookHandler_Prompted_LogsSessionAndTurnID(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	deps := newHandlerDeps(t, pool)
	// Deliberately NO installLinearFixture call for this organization --
	// mirrors newHandlerDeps' own doc comment ("never actually called: no
	// installation row exists ... so postAcknowledgment skips before any
	// HTTP call"): this test only cares about the LOG line, not the
	// (best-effort, separately tested above) outbound activity post.
	organizationID := "org-log-prompted-1"

	handler := linear.NewWebhookHandler(deps)

	agentSessionID := "agent-session-log-prompted-1"
	createdBody := agentSessionCreatedPayload(agentSessionID, organizationID)
	rec := postWebhook(t, handler, createdBody, "delivery-log-created-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("created status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if _, err := pool.Exec(ctx,
		`UPDATE turns SET status = 'completed' WHERE session_id = (SELECT session_id FROM linear_agent_sessions WHERE agent_session_id = $1)`,
		agentSessionID,
	); err != nil {
		t.Fatalf("mark fixture turn completed: %v", err)
	}

	var logBuf bytes.Buffer
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	promptedBody := agentSessionPromptedPayloadWithUser(agentSessionID, organizationID, "linear-log-user-1", "please continue")
	rec2 := postWebhook(t, handler, promptedBody, "delivery-log-prompted-1")
	if rec2.Code != http.StatusOK {
		t.Fatalf("prompted status = %d, want %d; body=%s", rec2.Code, http.StatusOK, rec2.Body.String())
	}

	agentSessions := narvipg.NewLinearAgentSessionStore(pool)
	mapping, err := agentSessions.GetByAgentSessionID(ctx, agentSessionID)
	if err != nil {
		t.Fatalf("GetByAgentSessionID: %v", err)
	}

	turns := narvipg.NewTurnStore(pool)
	allTurns, err := turns.ListForSession(ctx, mapping.SessionID)
	if err != nil {
		t.Fatalf("ListForSession: %v", err)
	}
	var replyTurnID pgtype.UUID
	for _, tn := range allTurns {
		if tn.Status != sqlcgen.TurnStatusCompleted {
			replyTurnID = tn.ID
		}
	}
	if !replyTurnID.Valid {
		t.Fatal("could not identify the reply's own new turn")
	}

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, mapping.SessionID.String()) {
		t.Errorf("log output missing session_id %q; got: %s", mapping.SessionID.String(), logOutput)
	}
	if !strings.Contains(logOutput, replyTurnID.String()) {
		t.Errorf("log output missing turn_id %q; got: %s", replyTurnID.String(), logOutput)
	}
	if !strings.Contains(logOutput, "linear: added turn") {
		t.Errorf("log output missing the success log line; got: %s", logOutput)
	}
}
