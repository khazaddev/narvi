//go:build integration

// Integration tests for TurnStore.RecordStepCostUSD (§25.15's own per-step cost
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
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sync/errgroup"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	appreviewtriage "github.com/khazaddev/narvi/internal/app/reviewtriage"
)

// stepID gives each increment its own idempotency key, which is what a
// real step_finish carries (§6.1's stepId, one per step). Two increments
// sharing a key are the SAME step redelivered and must sum to one.
func stepID(i int) string { return "prt_step_" + strconv.Itoa(i) }

// TestTurnStore_RecordStepCostUSD_ConcurrentSameTurn_SumsAllIncrements proves the
// accumulation is genuinely concurrency-safe at the real-SQL level (-race,
// real testcontainers Postgres, N real goroutines): N concurrent
// RecordStepCostUSD calls for the SAME processing turn must all land -- the
// turn's own final cost_usd must equal the exact sum of every increment,
// never fewer, which is what a Go-side read-modify-write (read cost_usd,
// add in Go, write it back) would silently produce under real
// concurrency by losing sibling increments to the read/write race that
// "SET x = x + $1", computed IN SQL and serialized by Postgres's own row
// lock, cannot lose.
func TestTurnStore_RecordStepCostUSD_ConcurrentSameTurn_SumsAllIncrements(t *testing.T) {
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

	// start gates every goroutine's own RecordStepCostUSD call so they all arrive
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
			_, err := turnStore.RecordStepCostUSD(ctx, sessionID, stepID(i), increment)
			return err
		})
	}
	close(start)
	if err := g.Wait(); err != nil {
		t.Fatalf("concurrent RecordStepCostUSD: %v", err)
	}

	// Compared as a NUMBER, never as the string Postgres happens to render.
	// The text form carries the column's scale ("0.200000" at scale 6), so a
	// string compare here would fail on a scale change that lost nothing --
	// and, worse, would pass if the scale silently truncated to a value that
	// still printed the way this test expected.
	var stored pgtype.Numeric
	if err := pool.QueryRow(ctx,
		`SELECT cost_usd FROM turns WHERE session_id = $1`, sessionID,
	).Scan(&stored); err != nil {
		t.Fatalf("query stored cost_usd: %v", err)
	}
	got, ok := appreviewtriage.NumericToFloat64(stored)
	if !ok {
		t.Fatalf("cost_usd is NULL after %d concurrent increments; want %v", n, n*increment)
	}
	if got != n*increment {
		t.Errorf("cost_usd = %v, want %v (%d concurrent increments of %v each, none lost)", got, n*increment, n, increment)
	}
}

// TestTurnStore_RecordStepCostUSD_SubCentIncrementsAccumulate pins the COLUMN
// SCALE, which is a different property from the concurrency test above and
// fails for a completely different reason. A single agent step routinely
// costs a fraction of a cent, and Postgres rounds each addition to the
// column's own scale BEFORE storing it -- so at scale 2 the increments
// never accumulate at all. Measured on real Postgres before this test
// existed: fifty steps at $0.004, twenty cents of genuine spend, summed to
// exactly 0.00. A cost column that reports free is worse than no column,
// and it reports free most confidently for the cheap high-step-count turns
// the figure matters most for.
// TestTurnStore_RecordStepCostUSD_ReplayAcrossTurnBoundary_ChargesOnce is
// the case the first key could not see. §6.1's sender buffers events and
// re-sends them on reconnect, and that replay can arrive after the turn it
// was emitted for has ended and the next one has begun. Keyed on
// (turn_id, step_id), the replay resolved a DIFFERENT turn, did not
// conflict, and charged the same step a second time -- measured at $10.00
// for one $5.00 step. An idempotency key derived from state that moves
// between the two deliveries it exists to tell apart is not one.
func TestTurnStore_RecordStepCostUSD_ReplayAcrossTurnBoundary_ChargesOnce(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)
	turnStore := narvipg.NewTurnStore(pool)

	first, err := turnStore.Create(ctx, sqlcgen.CreateTurnParams{SessionID: sessionID, Status: sqlcgen.TurnStatusProcessing})
	if err != nil {
		t.Fatalf("create first turn: %v", err)
	}

	const stepID = "prt_step_replayed"
	const cost = 5.00
	if _, err := turnStore.RecordStepCostUSD(ctx, sessionID, stepID, cost); err != nil {
		t.Fatalf("first delivery: %v", err)
	}

	// The turn boundary: the first turn terminalizes and the next one starts.
	if _, err := pool.Exec(ctx, `UPDATE turns SET status = 'completed' WHERE id = $1`, first.ID); err != nil {
		t.Fatalf("terminalize first turn: %v", err)
	}
	second, err := turnStore.Create(ctx, sqlcgen.CreateTurnParams{SessionID: sessionID, Status: sqlcgen.TurnStatusProcessing})
	if err != nil {
		t.Fatalf("create second turn: %v", err)
	}

	// The reconnect replay of the SAME step_finish, now landing mid-second-turn.
	if _, err := turnStore.RecordStepCostUSD(ctx, sessionID, stepID, cost); err != nil {
		t.Fatalf("replay: %v", err)
	}

	var total float64
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(cost_usd), 0)::float8 FROM turns WHERE session_id = $1`, sessionID,
	).Scan(&total); err != nil {
		t.Fatalf("sum session cost: %v", err)
	}
	if total != cost {
		t.Errorf("session charged %v in total for ONE $%v step delivered twice across a turn boundary; want %v", total, cost, cost)
	}

	// And it stayed on the turn that actually ran the step, not the new one.
	got, err := turnStore.Get(ctx, second.ID)
	if err != nil {
		t.Fatalf("get second turn: %v", err)
	}
	if v, ok := appreviewtriage.NumericToFloat64(got.CostUsd); ok {
		t.Errorf("the replay landed %v on the turn that started AFTER the step ran; want that turn untouched", v)
	}
}

func TestTurnStore_RecordStepCostUSD_SubCentIncrementsAccumulate(t *testing.T) {
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

	const steps = 50
	const increment = 0.004
	const want = steps * increment // $0.20, and not one cent of it is representable at scale 2

	for i := 0; i < steps; i++ {
		if _, err := turnStore.RecordStepCostUSD(ctx, sessionID, stepID(i), increment); err != nil {
			t.Fatalf("add cost %d: %v", i, err)
		}
	}

	stored, err := turnStore.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("read back turn: %v", err)
	}
	got, ok := appreviewtriage.NumericToFloat64(stored.CostUsd)
	if !ok {
		t.Fatalf("cost_usd is NULL after %d increments; want %v", steps, want)
	}
	// Exact, not approximate: NUMERIC is the whole reason this column is
	// not a float, so a scale wide enough to hold the addends must also
	// hold their sum exactly.
	if got != want {
		t.Fatalf("cost_usd = %v after %d increments of %v; want exactly %v -- a rounded-to-cents column silently reports cheap turns as free", got, steps, increment, want)
	}
}

// TestTurnStore_RecordStepCostUSD_FirstCallSetsFromNullNeverFromZero proves the
// COALESCE(cost_usd, 0) half of the guard: a freshly created turn's own
// cost_usd starts genuinely NULL (never a fabricated 0.00, §25.15's own
// "no cost yet must never render as free"), and the FIRST AddCostUSD call
// sets it from that NULL starting point -- not from an implicit zero the
// column never actually held.
func TestTurnStore_RecordStepCostUSD_FirstCallSetsFromNullNeverFromZero(t *testing.T) {
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

	affected, err := turnStore.RecordStepCostUSD(ctx, sessionID, "step-1", 1.23)
	if err != nil {
		t.Fatalf("RecordStepCostUSD: %v", err)
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
		t.Fatal("cost_usd is still NULL after RecordStepCostUSD, want 1.23")
	}
	if v != 1.23 {
		t.Errorf("cost_usd = %v, want 1.23", v)
	}
}

// TestTurnStore_RecordStepCostUSD_NoProcessingTurn_ZeroRowsAffected proves the
// guard's own "0 rows means no live target" contract: a session with no
// turn currently processing (none created at all here) must report 0
// rows affected, never an error and never a fabricated row.
func TestTurnStore_RecordStepCostUSD_NoProcessingTurn_ZeroRowsAffected(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	turnStore := narvipg.NewTurnStore(pool)
	affected, err := turnStore.RecordStepCostUSD(ctx, sessionID, "step-1", 1.00)
	if err != nil {
		t.Fatalf("RecordStepCostUSD: %v", err)
	}
	if affected != 0 {
		t.Errorf("affected = %d, want 0 (no turn currently processing for this session)", affected)
	}
}
