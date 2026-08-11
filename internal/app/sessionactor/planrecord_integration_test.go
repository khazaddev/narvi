//go:build integration

package sessionactor

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/adapters/outbound/slackapi"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// captureDefaultLoggerJSON temporarily replaces slog.Default() with a JSON
// handler writing into a *bytes.Buffer, restoring the original on cleanup --
// mirrors internal/app/outboxworker/builder_integration_test.go's own
// established "capture slog.Default() into a buffer" convention
// (TestPumpOnce_AttemptLogsCorrelationIDAndSessionID), also already reused
// by internal/adapters/inbound/httpapi/planapprove_integration_test.go's own
// identically-named helper for THIS SAME batch's M2 fix.
//
// CALLER MUST install this BEFORE the Actor under test is hydrated/spawned
// (i.e. before any GetOrSpawn call): platform.Logger(ctx) resolves
// slog.Default() exactly once, at hydrate.go's own hydrateAndAcquire time,
// and the result is cached onto the Actor struct (a.logger) for that
// Actor's whole lifetime -- installing the capture afterward would miss
// every log line the actor emits, since it would keep writing through the
// ORIGINAL logger it already captured.
func captureDefaultLoggerJSON(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	origLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(origLogger) })
	return &buf
}

// findLogEntry scans buf's own newline-delimited JSON log lines for the
// first one whose "msg" field equals wantMsg -- fails the test if none is
// found. Mirrors planapprove_integration_test.go's own identical helper.
func findLogEntry(t *testing.T, buf *bytes.Buffer, wantMsg string) map[string]any {
	t.Helper()
	for _, line := range bytes.Split(buf.Bytes(), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("unmarshal log line %q: %v", line, err)
		}
		if entry["msg"] == wantMsg {
			return entry
		}
	}
	t.Fatalf("no log line with msg %q found; full log output:\n%s", wantMsg, buf.String())
	return nil
}

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

	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", nil, nil, "", nil, false)
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

	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", nil, nil, "", nil, false)
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

			r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", nil, nil, "", nil, false)
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

	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", nil, nil, "", nil, false)
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

// getAuditLogRowsForResource fetches every audit_log row matching
// (resourceType, resourceID) -- this file's own assertion helper for the
// audit-fix batch's own "plan.superseded" audit row (see planrecord.go's
// own doc comment).
func getAuditLogRowsForResource(ctx context.Context, t *testing.T, pool *pgxpool.Pool, resourceType, resourceID string) []sqlcgen.AuditLog {
	t.Helper()
	rows, err := pool.Query(ctx,
		`SELECT id, actor_user_id, action, resource_type, resource_id, detail_json, correlation_id, created_at FROM audit_log WHERE resource_type = $1 AND resource_id = $2`,
		resourceType, resourceID)
	if err != nil {
		t.Fatalf("query audit_log rows: %v", err)
	}
	defer rows.Close()

	var out []sqlcgen.AuditLog
	for rows.Next() {
		var a sqlcgen.AuditLog
		if err := rows.Scan(&a.ID, &a.ActorUserID, &a.Action, &a.ResourceType, &a.ResourceID, &a.DetailJson, &a.CorrelationID, &a.CreatedAt); err != nil {
			t.Fatalf("scan audit_log row: %v", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate audit_log rows: %v", err)
	}
	return out
}

// countOutboxRowsForKind counts outbox rows for sessionID matching kind --
// this file's own twin of outboxenqueue_integration_test.go's own
// countOutboxRowsForSession, narrowed to one kind since a session with a
// stored Slack plan ref could in principle carry other outbox rows too.
func countOutboxRowsForKind(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sessionID pgtype.UUID, kind string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE session_id = $1 AND kind = $2`, sessionID, kind).Scan(&n); err != nil {
		t.Fatalf("count outbox rows for kind: %v", err)
	}
	return n
}

// TestCompleteProcessingTurn_Supersede_WithSlackRef_RecordsAuditLogAndEnqueuesOutbox
// proves the audit-fix batch's own L19/L6 fix: superseding v1 (which has a
// stored Slack message ref, simulating outboxworker's own planSlackNotifier.
// deliverApproval having already posted its approval-request message)
// records a "plan.superseded" audit_log row (system-triggered: an
// explicitly invalid/NULL actor_user_id, mirroring identitylink's own
// identical convention) AND enqueues exactly one
// ports.NotificationKindSlackPlanDecided outbox row carrying v1's own
// stored channel/ts, so the stale message's Approve/Reject buttons get
// stripped via a real chat.update rather than staying interactively (and
// confusingly) alive.
//
// Also proves this same batch's own LOW/observability fix: planrecord.go's
// own "sessionactor: plan superseded by a newer version" log line (added by
// commit bcc8746 alongside the audit_log row above) actually fires, with
// plan_id/session_id/superseded_by_version fields -- previously asserted
// nowhere. captureDefaultLoggerJSON is installed BEFORE GetOrSpawn below,
// not after, since platform.Logger(ctx) is resolved once at hydrate time
// and cached onto the Actor (see that helper's own doc comment).
func TestCompleteProcessingTurn_Supersede_WithSlackRef_RecordsAuditLogAndEnqueuesOutbox(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	sessionID := createTestSession(ctx, t, pool)
	if _, err := narvipg.NewSandboxStore(pool).Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	turnStore := narvipg.NewTurnStore(pool)
	plans := narvipg.NewPlanStore(pool)

	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", nil, nil, "", nil, false)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })

	// Installed BEFORE GetOrSpawn (hydrate time) -- see this test's own doc
	// comment and captureDefaultLoggerJSON's own doc comment above.
	buf := captureDefaultLoggerJSON(t)

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	createProcessingTurnWithPlanMode(ctx, t, turnStore, sessionID, true, nil)
	sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type: "execution_complete",
		Gen:  1,
		Raw:  executionCompleteRaw(t, sessionID.String(), 1, sandboxws.ExecutionCompleteOutcomeCompleted),
	})

	plansAfterV1 := listPlansForSession(ctx, t, pool, sessionID)
	if len(plansAfterV1) != 1 {
		t.Fatalf("after v1: len(plans) = %d, want 1", len(plansAfterV1))
	}
	v1 := plansAfterV1[0]

	const wantChannelID = "C-supersede"
	const wantMessageTS = "1700000000.000042"
	if err := plans.SetSlackMessageRef(ctx, v1.ID, wantChannelID, wantMessageTS); err != nil {
		t.Fatalf("seed slack message ref on v1: %v", err)
	}

	createProcessingTurnWithPlanMode(ctx, t, turnStore, sessionID, true, nil)
	sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type: "execution_complete",
		Gen:  1,
		Raw:  executionCompleteRaw(t, sessionID.String(), 1, sandboxws.ExecutionCompleteOutcomeCompleted),
	})

	plansAfterV2 := listPlansForSession(ctx, t, pool, sessionID)
	if len(plansAfterV2) != 2 {
		t.Fatalf("after v2: len(plans) = %d, want 2", len(plansAfterV2))
	}
	if plansAfterV2[0].Status != sqlcgen.PlanStatusSuperseded {
		t.Fatalf("v1 status = %q, want %q", plansAfterV2[0].Status, sqlcgen.PlanStatusSuperseded)
	}

	// The audit_log row.
	auditRows := getAuditLogRowsForResource(ctx, t, pool, "plan", v1.ID.String())
	if len(auditRows) != 1 {
		t.Fatalf("audit_log rows for superseded plan = %d, want exactly 1", len(auditRows))
	}
	auditRow := auditRows[0]
	if auditRow.Action != "plan.superseded" {
		t.Errorf("Action = %q, want %q", auditRow.Action, "plan.superseded")
	}
	if auditRow.ActorUserID.Valid {
		t.Errorf("ActorUserID.Valid = true, want false (NULL) -- this is a system-triggered transition, not a user action")
	}
	var detail map[string]any
	if err := json.Unmarshal(auditRow.DetailJson, &detail); err != nil {
		t.Fatalf("unmarshal detail_json: %v", err)
	}
	if detail["session_id"] != sessionID.String() {
		t.Errorf("detail_json[session_id] = %v, want %q", detail["session_id"], sessionID.String())
	}
	if v, ok := detail["superseded_by_version"].(float64); !ok || v != 2 {
		t.Errorf("detail_json[superseded_by_version] = %v, want 2", detail["superseded_by_version"])
	}

	// The supersession log line (LOW/observability fix -- previously had no
	// test coverage at all).
	logEntry := findLogEntry(t, buf, "sessionactor: plan superseded by a newer version")
	if got, _ := logEntry["plan_id"].(string); got != v1.ID.String() {
		t.Errorf("log line plan_id = %q, want %q", got, v1.ID.String())
	}
	if got, _ := logEntry["session_id"].(string); got != sessionID.String() {
		t.Errorf("log line session_id = %q, want %q", got, sessionID.String())
	}
	if got, ok := logEntry["superseded_by_version"].(float64); !ok || got != 2 {
		t.Errorf("log line superseded_by_version = %v, want 2", logEntry["superseded_by_version"])
	}

	// The outbox row.
	if n := countOutboxRowsForKind(ctx, t, pool, sessionID, string(ports.NotificationKindSlackPlanDecided)); n != 1 {
		t.Fatalf("outbox rows of kind %q = %d, want exactly 1", ports.NotificationKindSlackPlanDecided, n)
	}
	var payloadRaw []byte
	if err := pool.QueryRow(ctx, `SELECT payload FROM outbox WHERE session_id = $1 AND kind = $2`, sessionID, string(ports.NotificationKindSlackPlanDecided)).Scan(&payloadRaw); err != nil {
		t.Fatalf("query outbox payload: %v", err)
	}
	var payload slackapi.PlanDecidedPayload
	if err := json.Unmarshal(payloadRaw, &payload); err != nil {
		t.Fatalf("unmarshal outbox payload: %v", err)
	}
	if payload.ChannelID != wantChannelID || payload.MessageTS != wantMessageTS {
		t.Errorf("payload (channel, ts) = (%q, %q), want (%q, %q)", payload.ChannelID, payload.MessageTS, wantChannelID, wantMessageTS)
	}
	if payload.Text == "" {
		t.Error("payload.Text is empty, want an honest superseded-outcome message")
	}
}

// TestCompleteProcessingTurn_Supersede_WithoutSlackRef_NoOutboxRow proves the
// sibling case: a superseded plan that never had a Slack message posted for
// it (e.g. a Linear/GitHub/web-origin session) still gets its
// "plan.superseded" audit_log row, but enqueues NO outbox row and returns
// no error -- there is no stale Slack message to update.
func TestCompleteProcessingTurn_Supersede_WithoutSlackRef_NoOutboxRow(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	sessionID := createTestSession(ctx, t, pool)
	if _, err := narvipg.NewSandboxStore(pool).Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	turnStore := narvipg.NewTurnStore(pool)

	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", nil, nil, "", nil, false)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	createProcessingTurnWithPlanMode(ctx, t, turnStore, sessionID, true, nil)
	sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type: "execution_complete",
		Gen:  1,
		Raw:  executionCompleteRaw(t, sessionID.String(), 1, sandboxws.ExecutionCompleteOutcomeCompleted),
	})

	plansAfterV1 := listPlansForSession(ctx, t, pool, sessionID)
	if len(plansAfterV1) != 1 {
		t.Fatalf("after v1: len(plans) = %d, want 1", len(plansAfterV1))
	}
	v1 := plansAfterV1[0]
	// Deliberately NO SetSlackMessageRef call -- v1 never had a Slack
	// message posted for it.

	createProcessingTurnWithPlanMode(ctx, t, turnStore, sessionID, true, nil)
	sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type: "execution_complete",
		Gen:  1,
		Raw:  executionCompleteRaw(t, sessionID.String(), 1, sandboxws.ExecutionCompleteOutcomeCompleted),
	})

	plansAfterV2 := listPlansForSession(ctx, t, pool, sessionID)
	if len(plansAfterV2) != 2 {
		t.Fatalf("after v2: len(plans) = %d, want 2", len(plansAfterV2))
	}
	if plansAfterV2[0].Status != sqlcgen.PlanStatusSuperseded {
		t.Fatalf("v1 status = %q, want %q", plansAfterV2[0].Status, sqlcgen.PlanStatusSuperseded)
	}

	auditRows := getAuditLogRowsForResource(ctx, t, pool, "plan", v1.ID.String())
	if len(auditRows) != 1 {
		t.Fatalf("audit_log rows for superseded plan = %d, want exactly 1 (the audit row is unconditional, unlike the outbox row)", len(auditRows))
	}

	if n := countOutboxRowsForKind(ctx, t, pool, sessionID, string(ports.NotificationKindSlackPlanDecided)); n != 0 {
		t.Errorf("outbox rows of kind %q = %d, want 0 (v1 never had a stored Slack message ref)", ports.NotificationKindSlackPlanDecided, n)
	}
}
