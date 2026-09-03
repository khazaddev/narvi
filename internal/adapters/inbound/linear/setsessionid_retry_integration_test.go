//go:build integration

// Integration tests for the HIGH audit fix ("releasing the
// linear_agent_sessions claim after a SetSessionID failure can spawn a
// duplicate, independently-dispatched agent"): a forced SetSessionID
// failure must be retried (webhook.go's own setSessionIDWithRetry,
// retry.go) rather than answered by releasing either claim -- mirrors
// webhook_integration_test.go's own conventions (testcontainers Postgres, a
// real linear.NewWebhookHandler, synthetic real-shaped payloads), using
// Deps.SessionIDSetter (retry.go's own nil-safe test seam) to force a
// controlled number of SetSessionID failures against a REAL row, with no
// actually-dropped Postgres connection needed.
package linear_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/narvidev/narvi/internal/adapters/inbound/linear"
	"github.com/narvidev/narvi/internal/platform"
)

// linearAgentSessionSetSessionIDStore is the narrow slice of
// *postgres.LinearAgentSessionStore's own real surface these fakes wrap --
// named distinctly from linear's own unexported sessionIDSetter (retry.go)
// since this external test package cannot reference that unexported type
// by name, only satisfy it structurally. deps.AgentSessions (a
// *postgres.LinearAgentSessionStore) already implements this exactly.
type linearAgentSessionSetSessionIDStore interface {
	SetSessionID(ctx context.Context, agentSessionID string, sessionID pgtype.UUID) error
}

// failNTimesThenSucceedSetter wraps a real linearAgentSessionSetSessionIDStore
// (normally deps.AgentSessions itself), synthetically failing the first
// failCount calls before delegating to the real store -- lets these tests
// force webhook.go's own setSessionIDWithRetry through a controlled number
// of transient failures against a REAL linear_agent_sessions row, without
// needing an actually-dropped Postgres connection.
type failNTimesThenSucceedSetter struct {
	real      linearAgentSessionSetSessionIDStore
	failCount int

	mu    sync.Mutex
	calls int
}

func (f *failNTimesThenSucceedSetter) SetSessionID(ctx context.Context, agentSessionID string, sessionID pgtype.UUID) error {
	f.mu.Lock()
	f.calls++
	call := f.calls
	f.mu.Unlock()

	if call <= f.failCount {
		return errors.New("synthetic transient SetSessionID failure")
	}
	return f.real.SetSessionID(ctx, agentSessionID, sessionID)
}

func (f *failNTimesThenSucceedSetter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// alwaysFailSetter never delegates to a real store -- every call fails,
// simulating every retry attempt being exhausted.
type alwaysFailSetter struct {
	mu    sync.Mutex
	calls int
}

func (f *alwaysFailSetter) SetSessionID(ctx context.Context, agentSessionID string, sessionID pgtype.UUID) error {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return errors.New("synthetic permanent SetSessionID failure")
}

func (f *alwaysFailSetter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestWebhookHandler_Created_SetSessionIDRetries_SucceedsAfterTransientFailures
// proves the "retry, then succeed" half of the HIGH audit fix: a
// SetSessionID call that fails twice, then succeeds on its third attempt
// (platform.DefaultTimeouts().LinearSetSessionIDMaxAttempts == 3) results
// in the SAME real session correctly linked in linear_agent_sessions --
// no duplicate session, and the webhook-delivery claim is never released
// (this was never a failure from the webhook's own perspective).
func TestWebhookHandler_Created_SetSessionIDRetries_SucceedsAfterTransientFailures(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	deps := newHandlerDeps(t, pool)

	fakeSetter := &failNTimesThenSucceedSetter{real: deps.AgentSessions, failCount: 2}
	deps.SessionIDSetter = fakeSetter

	handler := linear.NewWebhookHandler(deps)

	agentSessionID := "agent-session-retry-success-1"
	organizationID := "org-retry-success-1"
	const deliveryID = "delivery-retry-success-1"
	body := agentSessionCreatedPayload(agentSessionID, organizationID)

	rec := postWebhook(t, handler, body, deliveryID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (a retry that eventually succeeds must never fail the webhook); body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	wantAttempts := platform.DefaultTimeouts().LinearSetSessionIDMaxAttempts
	if fakeSetter.callCount() != wantAttempts {
		t.Errorf("SetSessionID call count = %d, want %d (2 failures then a success)", fakeSetter.callCount(), wantAttempts)
	}

	var sessionCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE spawn_source = 'linear'`).Scan(&sessionCount); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessionCount != 1 {
		t.Fatalf("session count = %d, want exactly 1 (no duplicate session from the retried attach)", sessionCount)
	}

	var mappedSessionID string
	if err := pool.QueryRow(ctx,
		`SELECT session_id::text FROM linear_agent_sessions WHERE agent_session_id = $1`, agentSessionID,
	).Scan(&mappedSessionID); err != nil {
		t.Fatalf("query linear_agent_sessions: %v", err)
	}
	if mappedSessionID == "" {
		t.Error("linear_agent_sessions.session_id is empty, want the real session id linked once the retry succeeded")
	}

	// The webhook-delivery claim must NOT have been released -- this
	// delivery is fully, successfully processed.
	var deliveryRowCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM webhook_deliveries WHERE provider = 'linear' AND delivery_id = $1`, deliveryID,
	).Scan(&deliveryRowCount); err != nil {
		t.Fatalf("count webhook_deliveries: %v", err)
	}
	if deliveryRowCount != 1 {
		t.Errorf("webhook_deliveries row count = %d, want 1 (claim persists -- never released for a retry that succeeded)", deliveryRowCount)
	}
}

// TestWebhookHandler_Created_SetSessionIDExhaustsRetries_ClaimsNeverReleased
// proves the "exhausted retries" half of the HIGH audit fix: EVERY
// SetSessionID attempt failing must NOT release either claim and must
// answer 200 (not a failure code) -- releasing here (the PREVIOUS,
// incorrect behavior) would let Linear redeliver the identical `created`
// event, spawning a SECOND, independently-dispatched session for the same
// agent_session_id while the FIRST, real, already-dispatched session
// becomes permanently unreachable. Also proves the failure is logged at
// Error, for manual reconciliation.
func TestWebhookHandler_Created_SetSessionIDExhaustsRetries_ClaimsNeverReleased(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	deps := newHandlerDeps(t, pool)

	fakeSetter := &alwaysFailSetter{}
	deps.SessionIDSetter = fakeSetter

	handler := linear.NewWebhookHandler(deps)

	agentSessionID := "agent-session-retry-exhausted-1"
	organizationID := "org-retry-exhausted-1"
	const deliveryID = "delivery-retry-exhausted-1"
	body := agentSessionCreatedPayload(agentSessionID, organizationID)

	// syncLogBuffer (webhook_integration_test.go), not a bare
	// strings.Builder: the `created` event below still creates a real
	// session/turn and dispatches it (this test's own doc comment above:
	// "the session/turn genuinely exist and are already dispatched") --
	// the SAME fire-and-forget GetOrSpawn+EnsureDispatched trigger every
	// turn-creation call site uses, so the session's Actor can still be
	// mid-flight on its own background goroutine, logging through this
	// SAME redirected default logger, while this test's own goroutine
	// reads logBuf.String() below. See syncLogBuffer's own doc comment for
	// the full race (an identical instance of the one caught by -race in
	// CI run 30887614911, in this package's own
	// TestWebhookHandler_Prompted_LogsSessionAndTurnID).
	logBuf := &syncLogBuffer{}
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	rec := postWebhook(t, handler, body, deliveryID)

	// HIGH audit fix: exhausted retries must still respond 200 -- the
	// session/turn genuinely exist and are already dispatched; a non-2xx
	// here would trigger Linear's own redelivery of this SAME `created`
	// event, spawning a SECOND, independently-dispatched session for the
	// exact same agent_session_id (the failure mode this fix exists to
	// close).
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (exhausted SetSessionID retries must never trigger a redelivery-causing failure code); body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	wantAttempts := platform.DefaultTimeouts().LinearSetSessionIDMaxAttempts
	if fakeSetter.callCount() != wantAttempts {
		t.Errorf("SetSessionID call count = %d, want %d (every retry attempt exhausted)", fakeSetter.callCount(), wantAttempts)
	}

	if !strings.Contains(logBuf.String(), "manual reconciliation") {
		t.Errorf("expected an Error-level log line about manual reconciliation, got: %s", logBuf.String())
	}

	// The webhook-delivery claim must NOT be released -- redelivering this
	// SAME delivery id would replay the identical `created` payload and
	// spawn a SECOND, independently-dispatched session for the same
	// agent_session_id (this fix's own headline failure mode).
	var deliveryRowCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM webhook_deliveries WHERE provider = 'linear' AND delivery_id = $1`, deliveryID,
	).Scan(&deliveryRowCount); err != nil {
		t.Fatalf("count webhook_deliveries: %v", err)
	}
	if deliveryRowCount != 1 {
		t.Errorf("webhook_deliveries row count = %d, want 1 (claim must stay held, never released)", deliveryRowCount)
	}

	// The SEPARATE linear_agent_sessions claim must ALSO stay held (its
	// own session_id column simply never got linked) -- releasing it would
	// let a LATER, distinct event re-claim this SAME agent_session_id and
	// attempt to create a SECOND, colliding session.
	var agentSessionRowCount int
	var sessionIDIsNull bool
	if err := pool.QueryRow(ctx,
		`SELECT count(*), bool_and(session_id IS NULL) FROM linear_agent_sessions WHERE agent_session_id = $1`, agentSessionID,
	).Scan(&agentSessionRowCount, &sessionIDIsNull); err != nil {
		t.Fatalf("query linear_agent_sessions: %v", err)
	}
	if agentSessionRowCount != 1 {
		t.Fatalf("linear_agent_sessions row count = %d, want 1 (the claim itself must still exist, un-released)", agentSessionRowCount)
	}
	if !sessionIDIsNull {
		t.Error("linear_agent_sessions.session_id is set, want NULL (SetSessionID never actually succeeded)")
	}

	// The real session/turn this delivery already created must still
	// exist, exactly once -- exhausting retries must never roll back or
	// duplicate the already-committed, already-dispatched work.
	var sessionCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE spawn_source = 'linear'`).Scan(&sessionCount); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessionCount != 1 {
		t.Errorf("session count = %d, want exactly 1 (the real, dispatched session must remain -- never rolled back)", sessionCount)
	}
}
