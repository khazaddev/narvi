//go:build integration

package sessionactor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
)

// TestHandleSandboxEvent_GitSync_RepliesWithGitSyncComplete drives
// handleSandboxEvent directly through Actor.Send (mirroring
// TestHandleSandboxEvent_FullRoundTrip's own harness exactly) against a
// REAL Postgres instance, proving §3.4's ("gitstate in-sandbox", §3.4
// design section 6) own CP-side wiring end to end: a real git_sync event
// is (a) persisted generically like every other sandbox event, and (b)
// answered with a real sandboxws.GitSyncComplete command via
// SandboxCommander.SendCommand, carrying the SAME sessionId/gen the
// inbound event carried -- sent AFTER handleSandboxEvent's own ack reply,
// never gating it (git_sync carries no ackId at all).
func TestHandleSandboxEvent_GitSync_RepliesWithGitSyncComplete(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	sandboxStore := narvipg.NewSandboxStore(pool)
	if _, err := sandboxStore.Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	commander := &fakeSendCommander{}
	r := newDispatchTestRegistry(t, ctx, pool, nil, commander)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	raw := json.RawMessage(`{"type":"git_sync","messageId":"gs-1","sessionId":"` + sessionID.String() +
		`","gen":1,"repo":"narvi","status":"checkout","branch":"narvi/` + sessionID.String() + `"}`)

	reply := make(chan SandboxEventOutcome, 1)
	if err := a.Send(ctx, SandboxEvent{Type: "git_sync", Gen: 1, MessageID: "gs-1", Raw: raw, Reply: reply}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case outcome := <-reply:
		if !outcome.Persisted {
			t.Error("git_sync event: Persisted = false, want true")
		}
		if outcome.AckID != "" {
			t.Errorf("git_sync event: AckID = %q, want empty (best-effort event, no ackId)", outcome.AckID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for SandboxEventOutcome")
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM events WHERE session_id = $1 AND type = $2`,
		sessionID, "git_sync",
	).Scan(&n); err != nil {
		t.Fatalf("count git_sync events: %v", err)
	}
	if n != 1 {
		t.Errorf("git_sync event count = %d, want 1", n)
	}

	waitUntil(t, 5*time.Second, func() bool { return commander.callCount() == 1 })

	var cmd sandboxws.GitSyncComplete
	if err := json.Unmarshal(commander.lastPayload(), &cmd); err != nil {
		t.Fatalf("unmarshal SendCommand payload as sandboxws.GitSyncComplete: %v", err)
	}
	if cmd.Type != "git_sync_complete" {
		t.Errorf("GitSyncComplete.Type = %q, want %q", cmd.Type, "git_sync_complete")
	}
	if cmd.SessionId != sessionID.String() {
		t.Errorf("GitSyncComplete.SessionId = %q, want %q", cmd.SessionId, sessionID.String())
	}
	if cmd.Gen != 1 {
		t.Errorf("GitSyncComplete.Gen = %d, want 1", cmd.Gen)
	}
	if commander.sessions[0] != sessionID.String() {
		t.Errorf("SendCommand sessionID = %q, want %q", commander.sessions[0], sessionID.String())
	}
}
