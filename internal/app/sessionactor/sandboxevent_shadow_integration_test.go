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

// TestHandleSnapshotReadyEvent_UnreadableRepos_StampsSnapshotShadowFalse
// is the polarity case, and the one the first version of this stamp got
// backwards.
//
// The stamp reuses the outbox's egress-mode resolver, and that resolver
// falls toward "shadow" whenever it cannot read the session or parse its
// repos -- correctly, because for the outbox "shadow" means "suppress",
// which is always the safe answer. For THIS column "shadow" means the
// opposite kind of thing: true is a statement that the snapshot never
// held more than a read-only credential and is safe to restore into a
// shadow session, and the restore check refuses only when the bit is
// FALSE. So the resolver's fail-CLOSED value is the permissive one here,
// and stamping it directly turned a failure to read into a grant.
//
// A session with one promoted (live) repo and one whose clone URL is not
// owner/repo is the reachable version: session creation validates scheme
// and host only, so such a URL persists. The sandbox mints and caches a
// write-capable token for the live repo, and the unparseable one then
// makes the resolver say "shadow" for a reason that is not evidence.
func TestHandleSnapshotReadyEvent_UnreadableRepos_StampsSnapshotShadowFalse(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	sessionID := createTestSessionWithRepos(ctx, t, pool, pgtype.UUID{}, "repo", "https://github.com/acme/unreadable-stamp.git", "main")
	// Replace the repos with a pair whose second entry has a host but no
	// owner/repo path -- accepted at session creation, unparseable here.
	if _, err := pool.Exec(ctx, `UPDATE sessions SET repos = $2 WHERE id = $1`, sessionID,
		[]byte(`[{"name":"repo","url":"https://github.com/acme/unreadable-stamp.git","branch":"main"},{"name":"other","url":"https://github.com/acme","branch":null}]`)); err != nil {
		t.Fatalf("set session repos: %v", err)
	}
	if _, err := narvipg.NewRepoSettingsStore(pool).UpsertLiveEgressEnabled(ctx, "acme/unreadable-stamp", true); err != nil {
		t.Fatalf("promote repo to live egress: %v", err)
	}

	sandboxStore := seedSnapshottingSandbox(ctx, t, pool, sessionID, "cmd-msg-unreadable")

	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", nil, nil, "", nil, false)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	raw := json.RawMessage(`{"type":"snapshot_ready","messageId":"sr-unreadable-1","sessionId":"s","gen":1,"ackId":"snapshot_ready:sr-unreadable-1","snapshotId":"snap-unreadable-1","commandMessageId":"cmd-msg-unreadable"}`)
	outcome := sendSandboxEvent(ctx, t, a, SandboxEvent{Type: "snapshot_ready", Gen: 1, Raw: raw})
	if !outcome.Persisted {
		t.Fatal("snapshot_ready: Persisted = false, want true")
	}

	row, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if row.SnapshotSuppressedInShadow {
		t.Error("snapshot_suppressed_in_shadow = true, want false: the mode was NOT positively established, and an unread mode must never buy a snapshot the trusted value")
	}
}
