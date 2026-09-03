//go:build integration

package sessionactor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	narvipg "github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/app/ports"
)

// This file proves §3.4's ("gitstate in-sandbox", §14.1/§3.4 design
// section "Part D") own wire-contract threading against a REAL Postgres
// instance: a session created under a scoped Environment (path_scope set)
// produces a real spawn whose CreateSpec.SessionConfig.PathScope carries
// those exact patterns -- mirroring contractdrift_integration_test.go's
// own conventions (newTestPool, fakeSpawnProvider, sendEnsureDispatched,
// waitUntil, createTestEnvironment's own sibling helper there).

// createTestEnvironmentWithPathScope inserts an environments row directly
// (bypassing httpapi.CreateSession, which this package never imports)
// carrying a real, non-empty path_scope -- the sibling of
// createTestEnvironment (contractdrift_integration_test.go), which
// deliberately leaves path_scope NULL for its own, unrelated tests.
func createTestEnvironmentWithPathScope(ctx context.Context, t *testing.T, pool *pgxpool.Pool, pathScope []string) pgtype.UUID {
	t.Helper()

	pathScopeJSON, err := json.Marshal(pathScope)
	if err != nil {
		t.Fatalf("marshal path scope: %v", err)
	}

	env, err := narvipg.NewEnvironmentStore(pool).Create(ctx, sqlcgen.CreateEnvironmentParams{
		PathScope: pathScopeJSON,
	})
	if err != nil {
		t.Fatalf("create test environment with path scope: %v", err)
	}
	return env.ID
}

// createTestSessionWithEnvironment mirrors createTestSession
// (integration_helpers_test.go) with environmentID additionally set, so
// dispatch.go's planFreshSpawn/planRestore populate
// sessionRow.EnvironmentID from it -- the same field
// assembleSessionConfig's own new environmentPathScope reads.
func createTestSessionWithEnvironment(ctx context.Context, t *testing.T, pool *pgxpool.Pool, environmentID pgtype.UUID) pgtype.UUID {
	t.Helper()

	created, err := narvipg.NewSessionStore(pool).Create(ctx, sqlcgen.CreateSessionParams{
		SpawnSource:   sqlcgen.SessionSpawnSourceWeb,
		EnvironmentID: environmentID,
	})
	if err != nil {
		t.Fatalf("create test session with environment: %v", err)
	}
	return created.ID
}

// TestAssembleSessionConfig_PathScopeThreadedFromScopedEnvironment proves a
// session created under a scoped Environment (path_scope non-empty)
// produces a real spawn whose CreateSpec.SessionConfig.PathScope carries
// those exact patterns -- the wire-contract threading this Step's own
// brief calls "a real, necessary additive wire-contract extension" if
// SessionConfig didn't already carry Environment/PathScope information
// (it didn't, before this Step).
func TestAssembleSessionConfig_PathScopeThreadedFromScopedEnvironment(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	wantPatterns := []string{"/apps/web/*", "/contracts/*"}
	environmentID := createTestEnvironmentWithPathScope(ctx, t, pool, wantPatterns)
	sessionID := createTestSessionWithEnvironment(ctx, t, pool, environmentID)

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-scoped"}}
	r := newDispatchTestRegistry(t, ctx, pool, provider, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	waitUntil(t, 5*time.Second, func() bool { return provider.callCount() == 1 })

	spec := provider.lastSpec()
	if spec.SessionConfig.PathScope == nil {
		t.Fatal("SessionConfig.PathScope = nil, want the scoped Environment's own patterns")
	}
	got := []string(*spec.SessionConfig.PathScope)
	if len(got) != len(wantPatterns) {
		t.Fatalf("PathScope = %v, want %v", got, wantPatterns)
	}
	for i, want := range wantPatterns {
		if got[i] != want {
			t.Errorf("PathScope[%d] = %q, want %q", i, got[i], want)
		}
	}
}

// TestAssembleSessionConfig_UnscopedSession_PathScopeAbsent proves the
// overwhelming common case (no Environment attached at all) leaves
// SessionConfig.PathScope nil -- unchanged behavior, zero regression risk
// for every ordinary session.
func TestAssembleSessionConfig_UnscopedSession_PathScopeAbsent(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-unscoped"}}
	r := newDispatchTestRegistry(t, ctx, pool, provider, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	waitUntil(t, 5*time.Second, func() bool { return provider.callCount() == 1 })

	spec := provider.lastSpec()
	if spec.SessionConfig.PathScope != nil {
		t.Errorf("SessionConfig.PathScope = %v, want nil", spec.SessionConfig.PathScope)
	}
}
