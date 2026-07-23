//go:build integration

package sessionactor

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/linearapi"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/adapters/outbound/slackapi"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// This file proves Step 35's ("outbox delivery", §5.1) own enqueue-side
// wiring: a slack/github/linear-origin session's turn completion writes
// exactly one correctly-shaped outbox row, and a web-origin session's
// completion writes none -- see outboxenqueue.go's own doc comment for the
// design this exercises.

// createTestSessionWithSpawnSource creates a bare session (no repos) with
// the given spawnSource -- mirrors createTestSession's own shape, just
// parameterized on the one field this file's own tests vary.
func createTestSessionWithSpawnSource(ctx context.Context, t *testing.T, pool *pgxpool.Pool, spawnSource sqlcgen.SessionSpawnSource) pgtype.UUID {
	t.Helper()
	created, err := narvipg.NewSessionStore(pool).Create(ctx, sqlcgen.CreateSessionParams{
		SpawnSource: spawnSource,
	})
	if err != nil {
		t.Fatalf("create test session with spawn source %q: %v", spawnSource, err)
	}
	return created.ID
}

// countOutboxRowsForSession counts every outbox row for sessionID.
func countOutboxRowsForSession(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sessionID pgtype.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE session_id = $1`, sessionID).Scan(&n); err != nil {
		t.Fatalf("count outbox rows: %v", err)
	}
	return n
}

// getSoleOutboxRowForSession fetches the single outbox row for sessionID,
// failing the test if there isn't exactly one.
func getSoleOutboxRowForSession(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sessionID pgtype.UUID) sqlcgen.Outbox {
	t.Helper()
	if n := countOutboxRowsForSession(ctx, t, pool, sessionID); n != 1 {
		t.Fatalf("outbox row count for session = %d, want exactly 1", n)
	}

	rows, err := pool.Query(ctx, `SELECT id, session_id, kind, payload, status, attempts, next_attempt_at, delivered_at, last_error, created_at FROM outbox WHERE session_id = $1`, sessionID)
	if err != nil {
		t.Fatalf("query outbox row: %v", err)
	}
	defer rows.Close()

	var row sqlcgen.Outbox
	if !rows.Next() {
		t.Fatal("no outbox row found")
	}
	if err := rows.Scan(&row.ID, &row.SessionID, &row.Kind, &row.Payload, &row.Status, &row.Attempts, &row.NextAttemptAt, &row.DeliveredAt, &row.LastError, &row.CreatedAt); err != nil {
		t.Fatalf("scan outbox row: %v", err)
	}
	return row
}

// TestCompleteProcessingTurn_SlackOrigin_EnqueuesExactlyOneSlackOutboxRow
// proves a slack-origin session's SUCCESSFUL turn completion enqueues
// exactly one 'slack'-kind outbox row, shaped as slackapi.Payload, carrying
// the session's own reverse-looked-up channel/thread address.
func TestCompleteProcessingTurn_SlackOrigin_EnqueuesExactlyOneSlackOutboxRow(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	sessionID := createTestSessionWithSpawnSource(ctx, t, pool, sqlcgen.SessionSpawnSourceSlack)

	if _, err := narvipg.NewSandboxStore(pool).Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	turnStore := narvipg.NewTurnStore(pool)
	createProcessingTurn(ctx, t, turnStore, sessionID)

	if _, ok, err := narvipg.NewSlackThreadSessionStore(pool).Claim(ctx, "C123", "1700000000.000100", sessionID); err != nil || !ok {
		t.Fatalf("claim slack thread session: ok=%v err=%v", ok, err)
	}

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

	row := getSoleOutboxRowForSession(ctx, t, pool, sessionID)
	if row.Kind != string(ports.NotificationKindSlack) {
		t.Errorf("Kind = %q, want %q", row.Kind, ports.NotificationKindSlack)
	}
	if row.Status != sqlcgen.OutboxStatusPending {
		t.Errorf("Status = %q, want %q", row.Status, sqlcgen.OutboxStatusPending)
	}

	var payload slackapi.Payload
	if err := json.Unmarshal(row.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload as slackapi.Payload: %v", err)
	}
	if payload.ChannelID != "C123" {
		t.Errorf("ChannelID = %q, want %q", payload.ChannelID, "C123")
	}
	if payload.ThreadTS != "1700000000.000100" {
		t.Errorf("ThreadTS = %q, want %q", payload.ThreadTS, "1700000000.000100")
	}
	if payload.Text == "" {
		t.Error("Text is empty, want a human-readable outcome message")
	}
}

// TestCompleteProcessingTurn_GitHubOrigin_EnqueuesExactlyOneGitHubOutboxRow
// proves a github-origin session's FAILED turn completion enqueues
// exactly one 'github'-kind outbox row, shaped as githubapi.Payload, with
// owner/repo split out of the session's own reverse-looked-up
// repo_full_name.
func TestCompleteProcessingTurn_GitHubOrigin_EnqueuesExactlyOneGitHubOutboxRow(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	sessionID := createTestSessionWithSpawnSource(ctx, t, pool, sqlcgen.SessionSpawnSourceGithub)

	if _, err := narvipg.NewSandboxStore(pool).Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	turnStore := narvipg.NewTurnStore(pool)
	createProcessingTurn(ctx, t, turnStore, sessionID)

	prSessions := narvipg.NewGitHubPRSessionStore(pool)
	if err := prSessions.EnsureRow(ctx, "acme/widgets", 42); err != nil {
		t.Fatalf("ensure github pr session row: %v", err)
	}
	if err := prSessions.SetSessionID(ctx, "acme/widgets", 42, sessionID); err != nil {
		t.Fatalf("set github pr session id: %v", err)
	}

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
		Raw:  executionCompleteRaw(t, sessionID.String(), 1, sandboxws.ExecutionCompleteOutcomeFailed),
	})

	row := getSoleOutboxRowForSession(ctx, t, pool, sessionID)
	if row.Kind != string(ports.NotificationKindGitHub) {
		t.Errorf("Kind = %q, want %q", row.Kind, ports.NotificationKindGitHub)
	}

	var payload githubapi.Payload
	if err := json.Unmarshal(row.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload as githubapi.Payload: %v", err)
	}
	if payload.Owner != "acme" || payload.Repo != "widgets" {
		t.Errorf("Owner/Repo = %q/%q, want %q/%q", payload.Owner, payload.Repo, "acme", "widgets")
	}
	if payload.PRNumber != 42 {
		t.Errorf("PRNumber = %d, want 42", payload.PRNumber)
	}
	if payload.Text == "" {
		t.Error("Text is empty, want a human-readable outcome message")
	}
}

// TestCompleteProcessingTurn_LinearOrigin_EnqueuesExactlyOneLinearOutboxRow
// proves a linear-origin session's SUCCESSFUL turn completion enqueues
// exactly one 'linear'-kind outbox row, shaped as linearapi.Payload, with
// Success=true.
func TestCompleteProcessingTurn_LinearOrigin_EnqueuesExactlyOneLinearOutboxRow(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	sessionID := createTestSessionWithSpawnSource(ctx, t, pool, sqlcgen.SessionSpawnSourceLinear)

	if _, err := narvipg.NewSandboxStore(pool).Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	turnStore := narvipg.NewTurnStore(pool)
	createProcessingTurn(ctx, t, turnStore, sessionID)

	agentSessions := narvipg.NewLinearAgentSessionStore(pool)
	if _, err := agentSessions.Claim(ctx, "agent-session-1", "org-1"); err != nil {
		t.Fatalf("claim linear agent session: %v", err)
	}
	if err := agentSessions.SetSessionID(ctx, "agent-session-1", sessionID); err != nil {
		t.Fatalf("set linear agent session id: %v", err)
	}

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

	row := getSoleOutboxRowForSession(ctx, t, pool, sessionID)
	if row.Kind != string(ports.NotificationKindLinear) {
		t.Errorf("Kind = %q, want %q", row.Kind, ports.NotificationKindLinear)
	}

	var payload linearapi.Payload
	if err := json.Unmarshal(row.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.AgentSessionID != "agent-session-1" {
		t.Errorf("AgentSessionID = %q, want %q", payload.AgentSessionID, "agent-session-1")
	}
	if payload.OrganizationID != "org-1" {
		t.Errorf("OrganizationID = %q, want %q", payload.OrganizationID, "org-1")
	}
	if !payload.Success {
		t.Error("Success = false, want true for a completed turn")
	}
}

// TestCompleteProcessingTurn_WebOrigin_EnqueuesNoOutboxRow proves a
// 'web'-origin session's turn completion enqueues NOTHING -- there is no
// external channel to notify.
func TestCompleteProcessingTurn_WebOrigin_EnqueuesNoOutboxRow(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	sessionID := createTestSession(ctx, t, pool)

	if _, err := narvipg.NewSandboxStore(pool).Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	turnStore := narvipg.NewTurnStore(pool)
	createProcessingTurn(ctx, t, turnStore, sessionID)

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

	if n := countOutboxRowsForSession(ctx, t, pool, sessionID); n != 0 {
		t.Errorf("outbox row count for web-origin session = %d, want 0", n)
	}
}
