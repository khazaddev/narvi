//go:build integration

package sessionactor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
)

// TestTurnDeadlineTimerFired_FullRoundTrip seeds a session with a single
// turn already Processing and long overdue against a tiny injected
// TurnDeadline, fires TimerFired{Name: TimerTurnDeadline} through a real
// Actor, and confirms -- reading everything back from Postgres, not from
// in-memory state -- that: the turn transitioned to Failed with
// completed_at set; a synthetic execution_complete event was appended;
// the session's derived status/failure_reason became Failed/Timeout; and
// the turn_deadline timer itself was deleted (the handler's own
// re-arm-or-delete contract).
func TestTurnDeadlineTimerFired_FullRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	timeouts := platform.DefaultTimeouts()
	timeouts.TurnDeadline = 50 * time.Millisecond // tiny, injected -- not the real 60m default

	turnStore := narvipg.NewTurnStore(pool)
	created, err := turnStore.Create(ctx, sqlcgen.CreateTurnParams{
		SessionID: sessionID,
		Status:    sqlcgen.TurnStatusPending,
	})
	if err != nil {
		t.Fatalf("create turn: %v", err)
	}

	dispatchedAt := time.Now().Add(-1 * time.Hour) // comfortably past the tiny deadline
	if _, err := turnStore.UpdateStatus(ctx, sqlcgen.UpdateTurnStatusParams{
		ID:           created.ID,
		Status:       sqlcgen.TurnStatusProcessing,
		DispatchedAt: pgtype.Timestamptz{Time: dispatchedAt, Valid: true},
	}); err != nil {
		t.Fatalf("move turn to processing: %v", err)
	}

	r := NewRegistry(ctx, pool, timeouts)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	if err := a.Send(ctx, TimerFired{Name: TimerTurnDeadline}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	waitUntil(t, 5*time.Second, func() bool {
		got, err := turnStore.Get(ctx, created.ID)
		return err == nil && got.Status == sqlcgen.TurnStatusFailed
	})

	gotTurn, err := turnStore.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get turn: %v", err)
	}
	if gotTurn.Status != sqlcgen.TurnStatusFailed {
		t.Fatalf("turn status = %q, want %q", gotTurn.Status, sqlcgen.TurnStatusFailed)
	}
	if !gotTurn.CompletedAt.Valid {
		t.Error("turn completed_at not set")
	}

	sessionStore := narvipg.NewSessionStore(pool)
	gotSession, err := sessionStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if gotSession.Status != sqlcgen.SessionStatusFailed {
		t.Errorf("session status = %q, want %q", gotSession.Status, sqlcgen.SessionStatusFailed)
	}
	if gotSession.FailureReason == nil || *gotSession.FailureReason != sqlcgen.SessionFailureReasonTimeout {
		t.Errorf("session failure_reason = %v, want %q", gotSession.FailureReason, sqlcgen.SessionFailureReasonTimeout)
	}

	timerStore := narvipg.NewTimerStore(pool)
	if _, err := timerStore.Get(ctx, sqlcgen.GetSessionTimerParams{SessionID: sessionID, Name: TimerTurnDeadline}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("turn_deadline timer get = %v, want pgx.ErrNoRows (handler must delete it once handled)", err)
	}

	var eventCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM events WHERE session_id = $1 AND type = 'execution_complete'`,
		sessionID,
	).Scan(&eventCount); err != nil {
		t.Fatalf("count execution_complete events: %v", err)
	}
	if eventCount != 1 {
		t.Errorf("execution_complete event count = %d, want 1 (synthetic completion, §3.3)", eventCount)
	}
}
