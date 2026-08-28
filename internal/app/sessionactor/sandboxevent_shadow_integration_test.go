//go:build integration

package sessionactor

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
)

// This file proves handleSnapshotReadyEvent's own new §30.4(3) stamp
// (sandboxevent.go): the effective egress mode is resolved exactly ONCE,
// at snapshot-confirmation time, and persisted onto
// sandboxes.snapshot_suppressed_in_shadow.

// seedSnapshottingSandbox moves a fresh sandbox row into Snapshotting with
// a matching pending_snapshot_message_id, mirroring
// TestHandleSnapshotReadyEvent_Normal_TransitionsToReadyAndPersistsID's own
// setup exactly.
func seedSnapshottingSandbox(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sessionID pgtype.UUID, pendingMessageID string) *narvipg.SandboxStore {
	t.Helper()
	sandboxStore := narvipg.NewSandboxStore(pool)
	if _, err := sandboxStore.Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if _, err := sandboxStore.UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
		SessionID: sessionID, Status: sqlcgen.SandboxStatusSnapshotting,
	}); err != nil {
		t.Fatalf("move sandbox to snapshotting: %v", err)
	}
	if _, err := sandboxStore.UpdatePendingSnapshotMessageID(ctx, sqlcgen.UpdateSandboxPendingSnapshotMessageIDParams{
		SessionID: sessionID, PendingSnapshotMessageID: &pendingMessageID,
	}); err != nil {
		t.Fatalf("seed pending snapshot message id: %v", err)
	}
	return sandboxStore
}

// TestHandleSnapshotReadyEvent_ShadowSession_StampsSnapshotShadowTrue
// proves a snapshot completing for a session whose own repo has never
// been promoted (egressmode.Resolve's own fail-closed shadow default) is
// stamped snapshot_suppressed_in_shadow = true.
func TestHandleSnapshotReadyEvent_ShadowSession_StampsSnapshotShadowTrue(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSessionWithRepos(ctx, t, pool, pgtype.UUID{}, "repo", "https://github.com/acme/shadow-stamp.git", "main")

	sandboxStore := seedSnapshottingSandbox(ctx, t, pool, sessionID, "cmd-msg-shadow")

	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", nil, nil, "", nil, false)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	raw := json.RawMessage(`{"type":"snapshot_ready","messageId":"sr-shadow-1","sessionId":"s","gen":1,"ackId":"snapshot_ready:sr-shadow-1","snapshotId":"snap-shadow-stamp-1","commandMessageId":"cmd-msg-shadow"}`)
	outcome := sendSandboxEvent(ctx, t, a, SandboxEvent{Type: "snapshot_ready", Gen: 1, Raw: raw})
	if !outcome.Persisted {
		t.Fatal("snapshot_ready: Persisted = false, want true")
	}

	row, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if !row.SnapshotSuppressedInShadow {
		t.Error("snapshot_suppressed_in_shadow = false, want true (this session's own repo was never promoted, so its effective mode is shadow)")
	}
}

// TestHandleSnapshotReadyEvent_LiveSession_StampsSnapshotShadowFalse is
// the mirror case: a repo explicitly promoted to live_egress_enabled=true
// stamps snapshot_suppressed_in_shadow = false.
func TestHandleSnapshotReadyEvent_LiveSession_StampsSnapshotShadowFalse(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSessionWithRepos(ctx, t, pool, pgtype.UUID{}, "repo", "https://github.com/acme/live-stamp.git", "main")

	if _, err := narvipg.NewRepoSettingsStore(pool).UpsertLiveEgressEnabled(ctx, "acme/live-stamp", true); err != nil {
		t.Fatalf("promote repo to live egress: %v", err)
	}

	sandboxStore := seedSnapshottingSandbox(ctx, t, pool, sessionID, "cmd-msg-live")

	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", nil, nil, "", nil, false)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	raw := json.RawMessage(`{"type":"snapshot_ready","messageId":"sr-live-1","sessionId":"s","gen":1,"ackId":"snapshot_ready:sr-live-1","snapshotId":"snap-live-stamp-1","commandMessageId":"cmd-msg-live"}`)
	outcome := sendSandboxEvent(ctx, t, a, SandboxEvent{Type: "snapshot_ready", Gen: 1, Raw: raw})
	if !outcome.Persisted {
		t.Fatal("snapshot_ready: Persisted = false, want true")
	}

	row, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if row.SnapshotSuppressedInShadow {
		t.Error("snapshot_suppressed_in_shadow = true, want false (this session's own repo is promoted to live)")
	}
}
