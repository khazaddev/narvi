//go:build integration

package sessionactor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
)

// TestHandleSandboxEvent_FullRoundTrip drives handleSandboxEvent directly
// through Actor.Send against a real Postgres instance (bypassing
// internal/adapters/inbound/wshub entirely -- that package's own
// integration tests cover the full WS-to-ack round trip; this one isolates
// handleSandboxEvent's own transactional behavior), proving: a
// non-critical, non-transitioning event still persists and bumps
// last_seen_at with no ack; a critical event's outcome carries its ackId
// verbatim; "ready" while Connecting transitions to Booting; "heartbeat"
// with a nil lastBootPhase while Booting transitions to Ready; "ready"
// while already Ready is a silent no-op (persisted, liveness bumped,
// status unchanged, no error); and a stale (too-low) gen is rejected
// outright -- not persisted, last_seen_at untouched, no ack.
func TestHandleSandboxEvent_FullRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	sandboxStore := narvipg.NewSandboxStore(pool)

	created, err := sandboxStore.Create(ctx, sessionID)
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if created.Gen != 1 {
		t.Fatalf("sandbox gen = %d, want 1", created.Gen)
	}

	moveTo := func(status sqlcgen.SandboxStatus) {
		t.Helper()
		if _, err := sandboxStore.UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
			SessionID: sessionID,
			Status:    status,
		}); err != nil {
			t.Fatalf("move sandbox to %s: %v", status, err)
		}
	}

	countEvents := func(eventType string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM events WHERE session_id = $1 AND type = $2`,
			sessionID, eventType,
		).Scan(&n); err != nil {
			t.Fatalf("count %s events: %v", eventType, err)
		}
		return n
	}

	r := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	send := func(t *testing.T, cmd SandboxEvent) SandboxEventOutcome {
		t.Helper()
		reply := make(chan SandboxEventOutcome, 1)
		cmd.Reply = reply
		if err := a.Send(ctx, cmd); err != nil {
			t.Fatalf("Send: %v", err)
		}
		select {
		case outcome := <-reply:
			return outcome
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for SandboxEventOutcome")
			return SandboxEventOutcome{}
		}
	}

	// --- (a) a non-critical, non-transitioning event still persists and
	// bumps last_seen_at, produces no ack. ---
	beforeToken, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}

	tokenRaw := json.RawMessage(`{"type":"token","messageId":"tok-1","sessionId":"s","gen":1}`)
	outcome := send(t, SandboxEvent{Type: "token", Gen: 1, Raw: tokenRaw})
	if !outcome.Persisted {
		t.Error("token event: Persisted = false, want true")
	}
	if outcome.AckID != "" {
		t.Errorf("token event: AckID = %q, want empty (non-critical type)", outcome.AckID)
	}
	if got := countEvents("token"); got != 1 {
		t.Errorf("token event count = %d, want 1", got)
	}

	afterToken, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if !afterToken.LastSeenAt.Valid {
		t.Fatal("last_seen_at not set after token event")
	}
	if beforeToken.LastSeenAt.Valid && !afterToken.LastSeenAt.Time.After(beforeToken.LastSeenAt.Time) {
		t.Errorf("last_seen_at did not advance: before=%v after=%v", beforeToken.LastSeenAt.Time, afterToken.LastSeenAt.Time)
	}
	if afterToken.Status != sqlcgen.SandboxStatusPending {
		t.Errorf("token event changed status to %s, want unchanged (%s)", afterToken.Status, sqlcgen.SandboxStatusPending)
	}

	// --- (b) a critical event persists AND its outcome carries the ackId
	// verbatim. ---
	critRaw := json.RawMessage(`{"type":"execution_complete","messageId":"m1","sessionId":"s","gen":1,"ackId":"execution_complete:m1","outcome":"completed"}`)
	outcome = send(t, SandboxEvent{Type: "execution_complete", Gen: 1, Raw: critRaw})
	if !outcome.Persisted {
		t.Error("execution_complete: Persisted = false, want true")
	}
	if outcome.AckID != "execution_complete:m1" {
		t.Errorf("execution_complete: AckID = %q, want %q", outcome.AckID, "execution_complete:m1")
	}
	if got := countEvents("execution_complete"); got != 1 {
		t.Errorf("execution_complete event count = %d, want 1", got)
	}

	// --- (c) "ready" while Connecting transitions to Booting. ---
	moveTo(sqlcgen.SandboxStatusConnecting)
	readyRaw := json.RawMessage(`{"type":"ready","messageId":"r1","sessionId":"s","gen":1}`)
	outcome = send(t, SandboxEvent{Type: "ready", Gen: 1, Raw: readyRaw})
	if !outcome.Persisted {
		t.Error("ready: Persisted = false, want true")
	}
	got, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if got.Status != sqlcgen.SandboxStatusBooting {
		t.Errorf("status after ready-while-connecting = %s, want %s", got.Status, sqlcgen.SandboxStatusBooting)
	}

	// --- (d) "heartbeat" with nil lastBootPhase while Booting transitions
	// to Ready. ---
	hbRaw := json.RawMessage(`{"type":"heartbeat","messageId":"h1","sessionId":"s","gen":1,"conversationId":null,"lastBootPhase":null}`)
	outcome = send(t, SandboxEvent{Type: "heartbeat", Gen: 1, Raw: hbRaw, LastBootPhase: nil})
	if !outcome.Persisted {
		t.Error("heartbeat: Persisted = false, want true")
	}
	got, err = sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if got.Status != sqlcgen.SandboxStatusReady {
		t.Errorf("status after heartbeat-while-booting = %s, want %s", got.Status, sqlcgen.SandboxStatusReady)
	}

	// --- (e) "ready" while ALREADY Ready is a silent no-op: event still
	// persisted, last_seen_at still bumped, status stays Ready, no error. ---
	beforeReady, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	time.Sleep(5 * time.Millisecond) // ensure a distinguishable later timestamp
	readyAgainRaw := json.RawMessage(`{"type":"ready","messageId":"r2","sessionId":"s","gen":1}`)
	outcome = send(t, SandboxEvent{Type: "ready", Gen: 1, Raw: readyAgainRaw})
	if !outcome.Persisted {
		t.Error("second ready: Persisted = false, want true")
	}
	afterReady, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if afterReady.Status != sqlcgen.SandboxStatusReady {
		t.Errorf("status after redundant ready = %s, want unchanged %s", afterReady.Status, sqlcgen.SandboxStatusReady)
	}
	if !afterReady.LastSeenAt.Time.After(beforeReady.LastSeenAt.Time) {
		t.Errorf("last_seen_at did not advance on redundant ready: before=%v after=%v", beforeReady.LastSeenAt.Time, afterReady.LastSeenAt.Time)
	}
	if got := countEvents("ready"); got != 2 {
		t.Errorf("ready event count = %d, want 2 (both ready frames persisted)", got)
	}

	// --- (f) a stale (too-low) gen is NOT persisted, does NOT bump
	// last_seen_at, and produces no ack. ---
	beforeStale, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	staleRaw := json.RawMessage(`{"type":"heartbeat","messageId":"h-stale","sessionId":"s","gen":0}`)
	outcome = send(t, SandboxEvent{Type: "heartbeat", Gen: 0, Raw: staleRaw})
	if outcome.Persisted {
		t.Error("stale-gen event: Persisted = true, want false")
	}
	if outcome.AckID != "" {
		t.Errorf("stale-gen event: AckID = %q, want empty", outcome.AckID)
	}
	afterStale, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if !afterStale.LastSeenAt.Time.Equal(beforeStale.LastSeenAt.Time) {
		t.Errorf("last_seen_at moved on a stale-gen event: before=%v after=%v", beforeStale.LastSeenAt.Time, afterStale.LastSeenAt.Time)
	}
	if got := countEvents("heartbeat"); got != 1 {
		t.Errorf("heartbeat event count = %d, want 1 (the stale-gen one must not have been persisted)", got)
	}
}
