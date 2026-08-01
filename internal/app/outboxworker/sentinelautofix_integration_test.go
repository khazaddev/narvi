//go:build integration

// Integration test for Step 48's ("sentinels + suggestions", §17.2) own
// sentinel-auto-fix notifier (sentinelautofix.go), against a real Postgres
// instance -- gated behind the "integration" build tag, reusing this
// package's own newTestPool helper (builder_integration_test.go).
package outboxworker_test

import (
	"context"
	"encoding/json"
	"testing"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/outboxworker"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/platform"
)

// TestSentinelAutoFixNotifier_SpawnsChildSessionAndUpdatesStores proves
// the notifier's own real Deliver: a real child session is spawned
// (httpapi.SpawnChildSession), sentinel_fixes.fix_child_session_id is
// recorded, and every named finding moves to 'fix_pending'.
func TestSentinelAutoFixNotifier_SpawnsChildSessionAndUpdatesStores(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	sessions := narvipg.NewSessionStore(pool)
	turns := narvipg.NewTurnStore(pool)
	environments := narvipg.NewEnvironmentStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)
	sentinelFixes := narvipg.NewSentinelFixStore(pool)
	reviewFindings := narvipg.NewReviewFindingStore(pool)

	registry, err := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "http://localhost:8080", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = registry.Shutdown() })

	originSession, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceGithub})
	if err != nil {
		t.Fatalf("create origin session: %v", err)
	}

	repoFullName := "acme/notifier-test-repo"
	fix, err := sentinelFixes.Claim(ctx, repoFullName, 77, originSession.ID, "feature-fix-me")
	if err != nil {
		t.Fatalf("claim sentinel_fixes: %v", err)
	}

	const identityHash = "abc123abc123abc123abc123abc123abc123abc123abc123abc123abc123ab"
	if _, err := reviewFindings.Upsert(ctx, sqlcgen.UpsertReviewFindingParams{
		RepoFullName: repoFullName,
		PrNumber:     77,
		IdentityHash: identityHash,
		Severity:     "medium",
		FilePath:     "internal/foo/bar.go",
		Description:  "Missing test coverage.",
	}); err != nil {
		t.Fatalf("upsert review finding: %v", err)
	}

	notifier := outboxworker.NewSentinelAutoFixNotifier(pool, sessions, turns, environments, auditLog, registry, sentinelFixes, reviewFindings)

	payload, err := json.Marshal(ports.SentinelAutoFixPayload{
		SentinelFixID:         fix.ID.String(),
		RepoFullName:          repoFullName,
		OriginPRNumber:        77,
		OriginReviewSessionID: originSession.ID.String(),
		OriginHeadBranch:      "feature-fix-me",
		RepoName:              "widgets",
		RepoCloneURL:          "https://github.com/acme/widgets.git",
		FindingIdentityHashes: []string{identityHash},
		FindingDescriptions:   []string{"Missing test coverage."},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	if err := notifier.Deliver(ctx, ports.Notification{Kind: ports.NotificationKindSentinelAutoFix, Payload: payload}); err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}

	updatedFix, err := sentinelFixes.GetByID(ctx, fix.ID)
	if err != nil {
		t.Fatalf("get sentinel_fixes: %v", err)
	}
	if !updatedFix.FixChildSessionID.Valid {
		t.Fatal("FixChildSessionID is still invalid after Deliver, want a real child session id")
	}
	if updatedFix.Status != "spawned" {
		t.Errorf("Status = %q, want %q", updatedFix.Status, "spawned")
	}

	childSession, err := sessions.Get(ctx, updatedFix.FixChildSessionID)
	if err != nil {
		t.Fatalf("get child session: %v", err)
	}
	if childSession.ProvenanceTag == nil || *childSession.ProvenanceTag != "sentinel_auto_fix" {
		t.Errorf("child session ProvenanceTag = %v, want %q", childSession.ProvenanceTag, "sentinel_auto_fix")
	}
	if childSession.SpawnDepth != 1 {
		t.Errorf("child session SpawnDepth = %d, want 1", childSession.SpawnDepth)
	}
	if !childSession.ParentSessionID.Valid || childSession.ParentSessionID != originSession.ID {
		t.Errorf("child session ParentSessionID = %v, want %v", childSession.ParentSessionID, originSession.ID)
	}

	finding, err := reviewFindings.Get(ctx, repoFullName, 77, identityHash)
	if err != nil {
		t.Fatalf("get review finding: %v", err)
	}
	if finding.Status != "fix_pending" {
		t.Errorf("finding Status = %q, want %q", finding.Status, "fix_pending")
	}
	if !finding.FixChildSessionID.Valid || finding.FixChildSessionID != updatedFix.FixChildSessionID {
		t.Errorf("finding FixChildSessionID = %v, want %v", finding.FixChildSessionID, updatedFix.FixChildSessionID)
	}

	// Idempotency: a redelivered/retried outbox entry must never spawn a
	// SECOND child session for the SAME claim.
	if err := notifier.Deliver(ctx, ports.Notification{Kind: ports.NotificationKindSentinelAutoFix, Payload: payload}); err != nil {
		t.Fatalf("second Deliver() error = %v", err)
	}
	refetched, err := sentinelFixes.GetByID(ctx, fix.ID)
	if err != nil {
		t.Fatalf("get sentinel_fixes after second deliver: %v", err)
	}
	if refetched.FixChildSessionID != updatedFix.FixChildSessionID {
		t.Errorf("FixChildSessionID changed after a redelivered outbox entry: %v -> %v, want it unchanged (idempotent, never a second spawn)",
			updatedFix.FixChildSessionID, refetched.FixChildSessionID)
	}
}
