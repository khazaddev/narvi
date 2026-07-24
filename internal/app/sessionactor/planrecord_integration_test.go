//go:build integration

package sessionactor

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
)

// This file proves Step 37's ("plan mode, web", §8.1/§12.2 item 3) own
// plan-row-creation hook (planrecord.go, called from pushpr.go's
// completeProcessingTurn) against a REAL Postgres instance -- mirroring
// outboxenqueue_integration_test.go's own established house style
// (createProcessingTurn direct-seed, sendSandboxEventForTest,
// executionCompleteRaw) exactly.

// createProcessingTurnWithPlanMode is createProcessingTurn's own
// plan_mode/modelID-parameterized twin -- this file's own only caller
// needs both, unlike every other existing caller of createProcessingTurn.
func createProcessingTurnWithPlanMode(ctx context.Context, t *testing.T, turns *narvipg.TurnStore, sessionID pgtype.UUID, planMode bool, modelID *string) sqlcgen.Turn {
	t.Helper()
	created, err := turns.Create(ctx, sqlcgen.CreateTurnParams{
		SessionID: sessionID,
		Status:    sqlcgen.TurnStatusProcessing,
		PlanMode:  planMode,
		ModelID:   modelID,
	})
	if err != nil {
		t.Fatalf("create processing turn (planMode=%v): %v", planMode, err)
	}
	return created
}

// listPlansForSession fetches every plans row for sessionID, ordered by
// version -- this file's own assertion helper.
func listPlansForSession(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sessionID pgtype.UUID) []sqlcgen.Plan {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT id, session_id, turn_id, version, status, plan_model_id, created_at, decided_at, decided_by FROM plans WHERE session_id = $1 ORDER BY version`, sessionID)
	if err != nil {
		t.Fatalf("query plans: %v", err)
	}
	defer rows.Close()

	var out []sqlcgen.Plan
	for rows.Next() {
		var p sqlcgen.Plan
		if err := rows.Scan(&p.ID, &p.SessionID, &p.TurnID, &p.Version, &p.Status, &p.PlanModelID, &p.CreatedAt, &p.DecidedAt, &p.DecidedBy); err != nil {
			t.Fatalf("scan plan row: %v", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate plan rows: %v", err)
	}
	return out
}

// TestCompleteProcessingTurn_PlanModeTurn_CreatesExactlyOnePlanRow proves a
// plan_mode=true turn's SUCCESSFUL completion creates exactly one plans
// row: version 1, status awaiting_approval, turn_id the completing turn's
// own id, plan_model_id copied from that turn's own model_id.
func TestCompleteProcessingTurn_PlanModeTurn_CreatesExactlyOnePlanRow(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	sessionID := createTestSession(ctx, t, pool)
	if _, err := narvipg.NewSandboxStore(pool).Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	turnStore := narvipg.NewTurnStore(pool)
	planModel := "anthropic/claude-opus-4-8"
	turn1 := createProcessingTurnWithPlanMode(ctx, t, turnStore, sessionID, true, &planModel)

	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type: "execution_complete",
		Gen:  1,
		Raw:  executionCompleteRaw(t, sessionID.String(), 1, sandboxws.ExecutionCompleteOutcomeCompleted),
	})

	plans := listPlansForSession(ctx, t, pool, sessionID)
	if len(plans) != 1 {
		t.Fatalf("len(plans) = %d, want exactly 1", len(plans))
	}
	got := plans[0]
	if got.Version != 1 {
		t.Errorf("Version = %d, want 1", got.Version)
	}
	if got.Status != sqlcgen.PlanStatusAwaitingApproval {
		t.Errorf("Status = %q, want %q", got.Status, sqlcgen.PlanStatusAwaitingApproval)
	}
	if got.TurnID != turn1.ID {
		t.Errorf("TurnID = %v, want %v", got.TurnID, turn1.ID)
	}
	if got.PlanModelID == nil || *got.PlanModelID != planModel {
		t.Errorf("PlanModelID = %v, want %q", got.PlanModelID, planModel)
	}
}

// TestCompleteProcessingTurn_NonPlanModeTurn_CreatesNoPlanRow proves a
// plan_mode=false turn's successful completion creates NO plans row at
// all.
func TestCompleteProcessingTurn_NonPlanModeTurn_CreatesNoPlanRow(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	sessionID := createTestSession(ctx, t, pool)
	if _, err := narvipg.NewSandboxStore(pool).Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	turnStore := narvipg.NewTurnStore(pool)
	createProcessingTurnWithPlanMode(ctx, t, turnStore, sessionID, false, nil)

	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type: "execution_complete",
		Gen:  1,
		Raw:  executionCompleteRaw(t, sessionID.String(), 1, sandboxws.ExecutionCompleteOutcomeCompleted),
	})

	if plans := listPlansForSession(ctx, t, pool, sessionID); len(plans) != 0 {
		t.Errorf("len(plans) = %d, want 0 (plan_mode was false)", len(plans))
	}
}

// TestCompleteProcessingTurn_PlanModeTurn_FailedOrCancelled_CreatesNoPlanRow
// proves a plan_mode=true turn that did NOT genuinely complete (failed, or
// was cancelled) creates NO plans row -- no plan was ever actually
// produced.
func TestCompleteProcessingTurn_PlanModeTurn_FailedOrCancelled_CreatesNoPlanRow(t *testing.T) {
	tests := []struct {
		name    string
		outcome sandboxws.ExecutionCompleteOutcome
	}{
		{name: "failed", outcome: sandboxws.ExecutionCompleteOutcomeFailed},
		{name: "cancelled", outcome: sandboxws.ExecutionCompleteOutcomeCancelled},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pool := newTestPool(t)

			sessionID := createTestSession(ctx, t, pool)
			if _, err := narvipg.NewSandboxStore(pool).Create(ctx, sessionID); err != nil {
				t.Fatalf("create sandbox: %v", err)
			}

			turnStore := narvipg.NewTurnStore(pool)
			createProcessingTurnWithPlanMode(ctx, t, turnStore, sessionID, true, nil)

			r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", nil, nil, "")
			if err != nil {
				t.Fatalf("NewRegistry: %v", err)
			}
			t.Cleanup(func() { _ = r.Shutdown() })

			a, err := r.GetOrSpawn(ctx, sessionID)
			if err != nil {
				t.Fatalf("GetOrSpawn: %v", err)
			}

			sendSandboxEventForTest(ctx, t, a, SandboxEvent{
				Type: "execution_complete",
				Gen:  1,
				Raw:  executionCompleteRaw(t, sessionID.String(), 1, tc.outcome),
			})

			if plans := listPlansForSession(ctx, t, pool, sessionID); len(plans) != 0 {
				t.Errorf("len(plans) = %d, want 0 (turn did not genuinely complete)", len(plans))
			}
		})
	}
}

// TestCompleteProcessingTurn_SecondPlanModeTurn_SupersedesPriorAwaitingApproval
// proves the normal v1 -> v2 supersede path end to end: a first plan_mode
// turn's completion creates plan v1 (awaiting_approval); a SECOND
// plan_mode turn on the SAME session then completing supersedes v1 and
// creates v2 (awaiting_approval) -- the exact mechanism a "request
// changes" turn (submitted via the existing POST .../turns endpoint,
// httpapi/turn_integration_test.go's own separate proof of THAT wiring)
// relies on once its own turn completes.
func TestCompleteProcessingTurn_SecondPlanModeTurn_SupersedesPriorAwaitingApproval(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	sessionID := createTestSession(ctx, t, pool)
	if _, err := narvipg.NewSandboxStore(pool).Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	turnStore := narvipg.NewTurnStore(pool)

	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	turn1 := createProcessingTurnWithPlanMode(ctx, t, turnStore, sessionID, true, nil)
	sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type: "execution_complete",
		Gen:  1,
		Raw:  executionCompleteRaw(t, sessionID.String(), 1, sandboxws.ExecutionCompleteOutcomeCompleted),
	})

	plansAfterV1 := listPlansForSession(ctx, t, pool, sessionID)
	if len(plansAfterV1) != 1 {
		t.Fatalf("after v1: len(plans) = %d, want 1", len(plansAfterV1))
	}

	turn2 := createProcessingTurnWithPlanMode(ctx, t, turnStore, sessionID, true, nil)
	sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type: "execution_complete",
		Gen:  1,
		Raw:  executionCompleteRaw(t, sessionID.String(), 1, sandboxws.ExecutionCompleteOutcomeCompleted),
	})

	plansAfterV2 := listPlansForSession(ctx, t, pool, sessionID)
	if len(plansAfterV2) != 2 {
		t.Fatalf("after v2: len(plans) = %d, want 2", len(plansAfterV2))
	}

	v1 := plansAfterV2[0]
	v2 := plansAfterV2[1]
	if v1.Version != 1 || v1.TurnID != turn1.ID {
		t.Errorf("v1 = %+v, want version 1 / turn_id %v", v1, turn1.ID)
	}
	if v1.Status != sqlcgen.PlanStatusSuperseded {
		t.Errorf("v1 Status = %q, want %q", v1.Status, sqlcgen.PlanStatusSuperseded)
	}
	if v2.Version != 2 || v2.TurnID != turn2.ID {
		t.Errorf("v2 = %+v, want version 2 / turn_id %v", v2, turn2.ID)
	}
	if v2.Status != sqlcgen.PlanStatusAwaitingApproval {
		t.Errorf("v2 Status = %q, want %q", v2.Status, sqlcgen.PlanStatusAwaitingApproval)
	}
}
