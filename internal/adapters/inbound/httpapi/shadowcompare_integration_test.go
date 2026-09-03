//go:build integration

package httpapi_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

type shadowComparisonTurnForTest struct {
	TurnId          string   `json:"turnId"`
	SessionId       string   `json:"sessionId"`
	ModelId         *string  `json:"modelId"`
	Effort          *string  `json:"effort"`
	Status          string   `json:"status"`
	DurationSeconds *float64 `json:"durationSeconds"`
}

type shadowComparisonReportForTest struct {
	TurnA shadowComparisonTurnForTest `json:"turnA"`
	TurnB shadowComparisonTurnForTest `json:"turnB"`
}

func TestGetShadowComparison_RequiresAuth(t *testing.T) {
	rig := newTestRig(t)

	status := rig.doJSON(t, http.MethodGet, "/api/admin/shadow-compare?turnA="+uuid.NewString()+"&turnB="+uuid.NewString(), nil, nil, "")
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

func TestGetShadowComparison_MemberForbidden(t *testing.T) {
	ctx := context.Background()
	rig := newTestRig(t)
	_, memberToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMember)

	status := rig.doJSON(t, http.MethodGet, "/api/admin/shadow-compare?turnA="+uuid.NewString()+"&turnB="+uuid.NewString(), nil, nil, memberToken)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d (§8.8: admin/maintainer only, same row as stop/resume session)", status, http.StatusForbidden)
	}
}

func TestGetShadowComparison_UnknownTurn_404(t *testing.T) {
	ctx := context.Background()
	rig := newTestRig(t)
	_, adminToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	status := rig.doJSON(t, http.MethodGet, "/api/admin/shadow-compare?turnA="+uuid.NewString()+"&turnB="+uuid.NewString(), nil, nil, adminToken)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

// TestGetShadowComparison_TwoRealTurns_ComparesSideBySide creates two
// real, differently-configured, completed turns and proves the response
// carries each one's own model/effort/status/duration independently,
// side by side -- the actual point of this tool.
func TestGetShadowComparison_TwoRealTurns_ComparesSideBySide(t *testing.T) {
	ctx := context.Background()
	rig := newTestRig(t)
	_, adminToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	sessionA, err := rig.sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceWeb})
	if err != nil {
		t.Fatalf("create session A: %v", err)
	}
	sessionB, err := rig.sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceWeb})
	if err != nil {
		t.Fatalf("create session B: %v", err)
	}

	modelA, effortA := "anthropic/claude-sonnet-4-5", "high"
	prompt := "implement the feature"
	turnA, err := rig.turns.Create(ctx, sqlcgen.CreateTurnParams{SessionID: sessionA.ID, Status: sqlcgen.TurnStatusCompleted, Prompt: &prompt, ModelID: &modelA, Effort: &effortA})
	if err != nil {
		t.Fatalf("create turn A: %v", err)
	}
	dispatchedAt := time.Now().Add(-time.Hour)
	completedAt := dispatchedAt.Add(42 * time.Second)
	if _, err := rig.turns.UpdateStatus(ctx, sqlcgen.UpdateTurnStatusParams{
		ID:           turnA.ID,
		Status:       sqlcgen.TurnStatusCompleted,
		DispatchedAt: pgtype.Timestamptz{Time: dispatchedAt, Valid: true},
		CompletedAt:  pgtype.Timestamptz{Time: completedAt, Valid: true},
	}); err != nil {
		t.Fatalf("update turn A status/timestamps: %v", err)
	}

	modelB, effortB := "openai/gpt-5.3-codex-spark", "xhigh"
	turnB, err := rig.turns.Create(ctx, sqlcgen.CreateTurnParams{SessionID: sessionB.ID, Status: sqlcgen.TurnStatusProcessing, Prompt: &prompt, ModelID: &modelB, Effort: &effortB})
	if err != nil {
		t.Fatalf("create turn B: %v", err)
	}

	var got shadowComparisonReportForTest
	status := rig.doJSON(t, http.MethodGet, "/api/admin/shadow-compare?turnA="+turnA.ID.String()+"&turnB="+turnB.ID.String(), nil, &got, adminToken)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	if got.TurnA.TurnId != turnA.ID.String() || got.TurnA.ModelId == nil || *got.TurnA.ModelId != modelA || got.TurnA.Effort == nil || *got.TurnA.Effort != effortA {
		t.Errorf("TurnA = %+v, want model=%q effort=%q", got.TurnA, modelA, effortA)
	}
	if got.TurnA.Status != "completed" {
		t.Errorf("TurnA.Status = %q, want %q", got.TurnA.Status, "completed")
	}
	if got.TurnA.DurationSeconds == nil || *got.TurnA.DurationSeconds < 41 || *got.TurnA.DurationSeconds > 43 {
		t.Errorf("TurnA.DurationSeconds = %v, want ~42", got.TurnA.DurationSeconds)
	}

	if got.TurnB.TurnId != turnB.ID.String() || got.TurnB.ModelId == nil || *got.TurnB.ModelId != modelB || got.TurnB.Effort == nil || *got.TurnB.Effort != effortB {
		t.Errorf("TurnB = %+v, want model=%q effort=%q", got.TurnB, modelB, effortB)
	}
	if got.TurnB.Status != "processing" {
		t.Errorf("TurnB.Status = %q, want %q", got.TurnB.Status, "processing")
	}
	if got.TurnB.DurationSeconds != nil {
		t.Errorf("TurnB.DurationSeconds = %v, want nil (still processing, never dispatched/completed timestamps set)", *got.TurnB.DurationSeconds)
	}
}
