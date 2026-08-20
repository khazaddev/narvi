//go:build integration

// Integration tests for Metrics ("decision inbox: read model +
// API", §16.2's own decision-latency metric) against a REAL Postgres
// instance -- gated behind the "integration" build tag, same package/
// newTestPool precedent as aggregate_integration_test.go.
package decisioninbox_test

import (
	"context"
	"testing"
	"time"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/decisioninbox"
	"github.com/khazaddev/narvi/internal/platform"
)

// TestMetrics_PositivePath proves Metrics computes a real, non-sentinel
// median for a genuinely decided plan (only the
// EMPTY case was previously asserted, so `return 0, 0, false, nil` passed
// unconditionally). A plan created then immediately approved via the
// real, guarded ApproveIfAwaitingApproval transition (decided_at = now(),
// server-side) gives a real, small, non-negative latency.
func TestMetrics_PositivePath(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()

	users := narvipg.NewUserStore(pool)
	sessions := narvipg.NewSessionStore(pool)
	turns := narvipg.NewTurnStore(pool)
	plans := narvipg.NewPlanStore(pool)

	approver, err := users.Create(ctx, sqlcgen.CreateUserParams{PrimaryEmail: "metrics-approver@example.com", DisplayName: "Approver", Role: sqlcgen.UserRoleMember})
	if err != nil {
		t.Fatalf("create approver user: %v", err)
	}
	session, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceWeb})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	turn, err := turns.Create(ctx, sqlcgen.CreateTurnParams{SessionID: session.ID, Status: sqlcgen.TurnStatusCompleted})
	if err != nil {
		t.Fatalf("create turn: %v", err)
	}
	plan, err := plans.Create(ctx, sqlcgen.CreatePlanParams{SessionID: session.ID, TurnID: turn.ID, Version: 1, Status: sqlcgen.PlanStatusAwaitingApproval})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	rows, err := plans.ApproveIfAwaitingApproval(ctx, plan.ID, session.ID, approver.ID)
	if err != nil {
		t.Fatalf("approve plan: %v", err)
	}
	if rows != 1 {
		t.Fatalf("ApproveIfAwaitingApproval() affected %d rows, want 1", rows)
	}

	deps := decisioninbox.Deps{Plans: plans, Timeouts: platform.DefaultTimeouts()}
	median, sampleSize, computed, err := decisioninbox.Metrics(ctx, deps, time.Now())
	if err != nil {
		t.Fatalf("Metrics() error = %v, want nil", err)
	}
	if !computed {
		t.Fatal("Metrics() computed = false, want true (one plan was genuinely decided)")
	}
	if sampleSize != 1 {
		t.Errorf("Metrics() sampleSize = %d, want 1", sampleSize)
	}
	if median < 0 {
		t.Errorf("Metrics() median = %v, want >= 0", median)
	}
	if median > time.Minute {
		t.Errorf("Metrics() median = %v, want a small, real create->decide gap (< 1 minute) -- suspiciously large, check created_at/decided_at wiring", median)
	}
}

// TestMetrics_WindowBoundary proves a decided plan OUTSIDE
// DecisionInboxLatencyWindow is excluded from the median (covering the
// window boundary): two decided plans are
// seeded, one just inside the window and one well outside it (raw SQL
// backdating -- ApproveIfAwaitingApproval itself can only ever set
// decided_at to the real, current now()).
func TestMetrics_WindowBoundary(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()

	sessions := narvipg.NewSessionStore(pool)
	turns := narvipg.NewTurnStore(pool)
	plans := narvipg.NewPlanStore(pool)
	timeouts := platform.DefaultTimeouts()

	session, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceWeb})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	turn, err := turns.Create(ctx, sqlcgen.CreateTurnParams{SessionID: session.ID, Status: sqlcgen.TurnStatusCompleted})
	if err != nil {
		t.Fatalf("create turn: %v", err)
	}

	// In-window: decided well inside DecisionInboxLatencyWindow (1 hour
	// before its own trailing edge).
	inWindow, err := plans.Create(ctx, sqlcgen.CreatePlanParams{SessionID: session.ID, TurnID: turn.ID, Version: 1, Status: sqlcgen.PlanStatusAwaitingApproval})
	if err != nil {
		t.Fatalf("create in-window plan: %v", err)
	}
	inWindowDecidedAt := time.Now().Add(-timeouts.DecisionInboxLatencyWindow + time.Hour)
	inWindowCreatedAt := inWindowDecidedAt.Add(-10 * time.Minute)
	if _, err := pool.Exec(ctx, `UPDATE plans SET status = 'approved', created_at = $2, decided_at = $3 WHERE id = $1`, inWindow.ID, inWindowCreatedAt, inWindowDecidedAt); err != nil {
		t.Fatalf("backdate in-window plan: %v", err)
	}

	// Out-of-window: decided well OUTSIDE the window (1 day past its
	// trailing edge) -- must be excluded entirely.
	outOfWindow, err := plans.Create(ctx, sqlcgen.CreatePlanParams{SessionID: session.ID, TurnID: turn.ID, Version: 2, Status: sqlcgen.PlanStatusAwaitingApproval})
	if err != nil {
		t.Fatalf("create out-of-window plan: %v", err)
	}
	outOfWindowDecidedAt := time.Now().Add(-timeouts.DecisionInboxLatencyWindow - 24*time.Hour)
	outOfWindowCreatedAt := outOfWindowDecidedAt.Add(-10 * time.Minute)
	if _, err := pool.Exec(ctx, `UPDATE plans SET status = 'approved', created_at = $2, decided_at = $3 WHERE id = $1`, outOfWindow.ID, outOfWindowCreatedAt, outOfWindowDecidedAt); err != nil {
		t.Fatalf("backdate out-of-window plan: %v", err)
	}

	deps := decisioninbox.Deps{Plans: plans, Timeouts: timeouts}
	median, sampleSize, computed, err := decisioninbox.Metrics(ctx, deps, time.Now())
	if err != nil {
		t.Fatalf("Metrics() error = %v, want nil", err)
	}
	if !computed {
		t.Fatal("Metrics() computed = false, want true (the in-window plan must be counted)")
	}
	if sampleSize != 1 {
		t.Errorf("Metrics() sampleSize = %d, want 1 (only the in-window plan -- the out-of-window one must be excluded)", sampleSize)
	}
	// This fixture's own created_at/decided_at gap is a KNOWN, exact 10 minutes (both
	// timestamps above are derived from the SAME inWindowDecidedAt value,
	// so the sub-microsecond remainder Postgres truncates on write is
	// identical for both, and the DIFFERENCE survives exactly) -- the one
	// fixture in this file with a known non-zero latency, previously
	// discarded (`_, sampleSize, computed, err := ...`) rather than
	// asserted. A single-sample median is just that one sample's own
	// duration, so this pins the actual VALUE Metrics computes, not only
	// that it computed something.
	if median != 10*time.Minute {
		t.Errorf("Metrics() median = %v, want exactly 10m (this fixture's own known created_at/decided_at gap)", median)
	}
}

// TestMetrics_1000RowCap proves maxRecentlyDecidedPlans (1000) actually
// bounds the query -- 1001 already-decided, in-window plan rows are bulk-seeded via
// raw SQL (reusing one session/turn -- plans carries no uniqueness
// constraint beyond its own partial "one awaiting_approval per session"
// index, which decided rows never touch) rather than 1001 round trips
// through the store's own API, to keep this test's own real wall-clock
// cost reasonable.
func TestMetrics_1000RowCap(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()

	sessions := narvipg.NewSessionStore(pool)
	turns := narvipg.NewTurnStore(pool)
	plans := narvipg.NewPlanStore(pool)

	session, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceWeb})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	turn, err := turns.Create(ctx, sqlcgen.CreateTurnParams{SessionID: session.ID, Status: sqlcgen.TurnStatusCompleted})
	if err != nil {
		t.Fatalf("create turn: %v", err)
	}

	const seededRows = 1001
	if _, err := pool.Exec(ctx, `
		INSERT INTO plans (session_id, turn_id, version, status, created_at, decided_at)
		SELECT $1, $2, gs, 'approved',
		       now() - interval '1 hour',
		       now() - interval '1 hour' + (gs * interval '1 second')
		FROM generate_series(1, $3) AS gs
	`, session.ID, turn.ID, seededRows); err != nil {
		t.Fatalf("bulk-seed decided plans: %v", err)
	}

	deps := decisioninbox.Deps{Plans: plans, Timeouts: platform.DefaultTimeouts()}
	_, sampleSize, computed, err := decisioninbox.Metrics(ctx, deps, time.Now())
	if err != nil {
		t.Fatalf("Metrics() error = %v, want nil", err)
	}
	if !computed {
		t.Fatal("Metrics() computed = false, want true")
	}
	if sampleSize != 1000 {
		t.Errorf("Metrics() sampleSize = %d, want 1000 (capped -- %d rows were seeded)", sampleSize, seededRows)
	}
}
