//go:build integration

package sessionactor

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
)

// TestTimerPump_ClaimsDueTimerWithoutRedelivering proves the pump's
// redelivery-safety mechanism (§2): a due timer is CLAIMED (fires_at
// pushed forward by TimerClaimDuration), never deleted, by one pump tick;
// an immediately following second tick must not re-claim/redeliver the
// same row before the claim window elapses.
func TestTimerPump_ClaimsDueTimerWithoutRedelivering(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	timeouts := platform.DefaultTimeouts()
	timeouts.TimerClaimDuration = 2 * time.Second

	// A timer name outside the 5 named ones (§2: "name is kept TEXT, not
	// an enum") -- deliberately, so this test isolates the PUMP's
	// claim/redeliver mechanics from any of the 5 handlers' own
	// behavior. handleTimerFired's dispatch defensively logs and ignores
	// an unrecognized name, so delivery is a harmless no-op here.
	const timerName = "integration_test_timer"

	timerStore := narvipg.NewTimerStore(pool)
	due := time.Now().Add(-1 * time.Minute)
	if _, err := timerStore.Upsert(ctx, sqlcgen.UpsertSessionTimerParams{
		SessionID: sessionID,
		Name:      timerName,
		FiresAt:   pgtype.Timestamptz{Time: due, Valid: true},
	}); err != nil {
		t.Fatalf("seed due timer: %v", err)
	}

	r, err := NewRegistry(ctx, pool, timeouts, nil, nil, nil, "", nil, nil, "", nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })

	beforeFirstTick := time.Now()
	if err := r.PumpOnce(ctx); err != nil {
		t.Fatalf("PumpOnce (first tick): %v", err)
	}

	afterFirst, err := timerStore.Get(ctx, sqlcgen.GetSessionTimerParams{SessionID: sessionID, Name: timerName})
	if err != nil {
		t.Fatalf("get timer after first tick: %v", err)
	}
	if !afterFirst.FiresAt.Time.After(beforeFirstTick) {
		t.Fatalf("fires_at after first claim = %v, want a time after %v (claimed forward, not left due or deleted)",
			afterFirst.FiresAt.Time, beforeFirstTick)
	}

	// A second, immediate tick must leave fires_at exactly as the first
	// claim left it -- if the row had been re-selected as due and
	// re-claimed, fires_at would have moved forward AGAIN.
	if err := r.PumpOnce(ctx); err != nil {
		t.Fatalf("PumpOnce (second tick): %v", err)
	}
	afterSecond, err := timerStore.Get(ctx, sqlcgen.GetSessionTimerParams{SessionID: sessionID, Name: timerName})
	if err != nil {
		t.Fatalf("get timer after second tick: %v", err)
	}
	if !afterSecond.FiresAt.Time.Equal(afterFirst.FiresAt.Time) {
		t.Fatalf("fires_at changed on a second immediate tick (%v -> %v) -- the still-claimed row was redelivered/re-claimed",
			afterFirst.FiresAt.Time, afterSecond.FiresAt.Time)
	}
}
