//go:build integration

// Integration tests for TurnStore.AddCostUSD (§25.15's own per-step cost
// accumulation) against a REAL Postgres instance under genuine concurrent
// transactions -- mirrors session_store_integration_test.go's own
// TestSessionStore_UpdateIntentDecisionIfNull_ConcurrentSameSession_
// ExactlyOneWinner errgroup-based concurrency-test shape exactly, adapted
// to a SUMMING guarded UPDATE ("SET cost_usd = COALESCE(cost_usd, 0) +
// $2") rather than a WRITE-ONCE one ("... WHERE x IS NULL"): the property
// under test here is that every concurrent caller's own increment lands,
// none silently overwritten or lost -- not that exactly one wins.
package postgres_test

import (
	"context"
	"testing"

	"golang.org/x/sync/errgroup"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	appreviewtriage "github.com/khazaddev/narvi/internal/app/reviewtriage"
)

// TestTurnStore_AddCostUSD_ConcurrentSameTurn_SumsAllIncrements proves the
// accumulation is genuinely concurrency-safe at the real-SQL level (-race,
// real testcontainers Postgres, N real goroutines): N concurrent
// AddCostUSD calls for the SAME processing turn must all land -- the
// turn's own final cost_usd must equal the exact sum of every increment,
// never fewer, which is what a Go-side read-modify-write (read cost_usd,
// add in Go, write it back) would silently produce under real
// concurrency by losing sibling increments to the read/write race that
// "SET x = x + $1", computed IN SQL and serialized by Postgres's own row
// lock, cannot lose.
func TestTurnStore_AddCostUSD_ConcurrentSameTurn_SumsAllIncrements(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	turnStore := narvipg.NewTurnStore(pool)
	if _, err := turnStore.Create(ctx, sqlcgen.CreateTurnParams{
		SessionID: sessionID,
		Status:    sqlcgen.TurnStatusProcessing,
	}); err != nil {
		t.Fatalf("create processing turn: %v", err)
	}

	const n = 20
	const increment = 0.01

	// start gates every goroutine's own AddCostUSD call so they all arrive
	// at the guarded UPDATE roughly together -- proving genuine
	// concurrency, not an accidental sequential ordering. Mirrors
	// TestSessionStore_UpdateIntentDecisionIfNull_ConcurrentSameSession_
	// ExactlyOneWinner's own identical close(start)-as-broadcast
	// convention.
	start := make(chan struct{})

	var g errgroup.Group
	for i := 0; i < n; i++ {
		g.Go(func() error {
			<-start
			_, err := turnStore.AddCostUSD(ctx, sessionID, increment)
			return err
		})
	}
	close(start)
	if err := g.Wait(); err != nil {
		t.Fatalf("concurrent AddCostUSD: %v", err)
	}

	var stored []byte
	if err := pool.QueryRow(ctx,
		`SELECT cost_usd FROM turns WHERE session_id = $1`, sessionID,
	).Scan(&stored); err != nil {
		t.Fatalf("query stored cost_usd: %v", err)
	}
	if string(stored) != "0.20" {
		t.Errorf("cost_usd = %s, want 0.20 (%d concurrent increments of %.2f each, none lost)", stored, n, increment)
	}
}

// TestTurnStore_AddCostUSD_FirstCallSetsFromNullNeverFromZero proves the
// COALESCE(cost_usd, 0) half of the guard: a freshly created turn's own
// cost_usd starts genuinely NULL (never a fabricated 0.00, §25.15's own
// "no cost yet must never render as free"), and the FIRST AddCostUSD call
// sets it from that NULL starting point -- not from an implicit zero the
// column never actually held.
func TestTurnStore_AddCostUSD_FirstCallSetsFromNullNeverFromZero(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	turnStore := narvipg.NewTurnStore(pool)
	created, err := turnStore.Create(ctx, sqlcgen.CreateTurnParams{
		SessionID: sessionID,
		Status:    sqlcgen.TurnStatusProcessing,
	})
	if err != nil {
		t.Fatalf("create processing turn: %v", err)
	}
	if v, ok := appreviewtriage.NumericToFloat64(created.CostUsd); ok {
		t.Fatalf("freshly created turn's cost_usd = %v (valid), want NULL", v)
	}

	affected, err := turnStore.AddCostUSD(ctx, sessionID, 1.23)
	if err != nil {
		t.Fatalf("AddCostUSD: %v", err)
	}
	if affected != 1 {
		t.Fatalf("affected = %d, want 1", affected)
	}

	got, err := turnStore.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get turn: %v", err)
	}
	v, ok := appreviewtriage.NumericToFloat64(got.CostUsd)
	if !ok {
		t.Fatal("cost_usd is still NULL after AddCostUSD, want 1.23")
	}
	if v != 1.23 {
		t.Errorf("cost_usd = %v, want 1.23", v)
	}
}

// TestTurnStore_AddCostUSD_NoProcessingTurn_ZeroRowsAffected proves the
// guard's own "0 rows means no live target" contract: a session with no
// turn currently processing (none created at all here) must report 0
// rows affected, never an error and never a fabricated row.
func TestTurnStore_AddCostUSD_NoProcessingTurn_ZeroRowsAffected(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	turnStore := narvipg.NewTurnStore(pool)
	affected, err := turnStore.AddCostUSD(ctx, sessionID, 1.00)
	if err != nil {
		t.Fatalf("AddCostUSD: %v", err)
	}
	if affected != 0 {
		t.Errorf("affected = %d, want 0 (no turn currently processing for this session)", affected)
	}
}
