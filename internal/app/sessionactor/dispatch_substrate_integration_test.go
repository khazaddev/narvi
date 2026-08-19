//go:build integration

// This file proves Step 74's own dispatch-time half of the "fail-closed,
// twice" rule (§27.5/§27.6, brief point A): tryPlanSpawn (dispatch.go's
// own refuseIfSubstrateUnsupported) refuses to spawn a docker-required or
// enforced-egress session against a provider that does not support it --
// and, critically, does so even when the session's own Environment row
// was created DIRECTLY against Postgres, bypassing httpapi.
// CreateSessionCore's own up-front check entirely (createTestEnvironmentWithDocker/
// createTestEnvironmentWithEgressPolicy below, mirroring
// sessionconfig_pathscope_integration_test.go's own
// createTestEnvironmentWithPathScope precedent) -- proving the dispatch-
// time check is a genuinely independent second guard, not merely a
// second call into logic the up-front check already exercised.
package sessionactor

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/ports"
)

// createTestEnvironmentWithDocker inserts an environments row directly
// (bypassing httpapi.CreateSession entirely -- this package never
// imports httpapi) carrying docker_required=true, the sibling of
// createTestEnvironmentWithPathScope (sessionconfig_pathscope_
// integration_test.go).
func createTestEnvironmentWithDocker(ctx context.Context, t *testing.T, pool *pgxpool.Pool) pgtype.UUID {
	t.Helper()
	env, err := narvipg.NewEnvironmentStore(pool).Create(ctx, sqlcgen.CreateEnvironmentParams{DockerRequired: true})
	if err != nil {
		t.Fatalf("create test environment with docker_required: %v", err)
	}
	return env.ID
}

// createTestEnvironmentWithEgressAllowlist inserts an environments row
// directly carrying egress_policy_mode='allowlist' -- the egress-policy
// sibling of createTestEnvironmentWithDocker above.
func createTestEnvironmentWithEgressAllowlist(ctx context.Context, t *testing.T, pool *pgxpool.Pool, allowlistJSON []byte) pgtype.UUID {
	t.Helper()
	mode := "allowlist"
	env, err := narvipg.NewEnvironmentStore(pool).Create(ctx, sqlcgen.CreateEnvironmentParams{
		EgressPolicyMode:      &mode,
		EgressPolicyAllowlist: allowlistJSON,
	})
	if err != nil {
		t.Fatalf("create test environment with egress_policy allowlist: %v", err)
	}
	return env.ID
}

// TestDispatch_RefusesDockerRequiredSessionWhenProviderUnsupported is a
// MUTATION-TESTABLE guard (Step 74 brief: "remove the dispatch re-check
// → a DIFFERENT named test must fail") and the dispatch-time half of "a
// test that disables one and shows the other still refuses, in both
// directions": the session's own Environment is created DIRECTLY against
// Postgres (createTestEnvironmentWithDocker), never through httpapi.
// CreateSessionCore's own up-front check -- so if THAT check were the
// only guard in the whole system, this session would sail straight
// through to a real spawn attempt. It must not: tryPlanSpawn's own
// dispatch-time re-check refuses it independently, proven here by
// asserting CreateSandbox is NEVER called even after EnsureDispatched
// has had a full, generous window to act.
func TestDispatch_RefusesDockerRequiredSessionWhenProviderUnsupported(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	environmentID := createTestEnvironmentWithDocker(ctx, t, pool)
	sessionID := createTestSessionWithEnvironment(ctx, t, pool, environmentID)

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	provider := &fakeSpawnProvider{
		nextRef:         ports.SandboxRef{ProviderID: "provider-should-never-be-called"},
		dockerSupported: false, // the provider does NOT support Docker
	}
	r := newDispatchTestRegistry(t, ctx, pool, provider, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	// A generous, fixed window (matching this package's own established
	// "prove a negative" convention, e.g. dispatch_integration_test.go's
	// own several 300ms sleeps) -- long enough that a real CreateSandbox
	// call, had the refusal NOT fired, would already have been recorded.
	time.Sleep(300 * time.Millisecond)

	if got := provider.callCount(); got != 0 {
		t.Errorf("provider.callCount() = %d, want 0 -- CreateSandbox must never be called for a docker-required session against a provider that does not support it", got)
	}

	// The sandbox row must never have advanced past its own absence --
	// no gen bump, no spawning claim -- confirming the refusal happened
	// BEFORE tryPlanSpawn's own token-mint/upsert write, not merely
	// before the provider call.
	sandboxes := narvipg.NewSandboxStore(pool)
	if _, err := sandboxes.Get(ctx, sessionID); err == nil {
		t.Error("sandboxes.Get succeeded -- want no sandbox row at all (the refusal must run before ANY spawn-claim write)")
	}
}

// TestDispatch_AllowsDockerRequiredSessionWhenProviderSupported is the
// refusal test's own positive control, proving the dispatch-time check
// is a real gate (not something that happens to always refuse): the
// identical Environment/session against a provider that DOES report
// DockerInSandbox support spawns normally.
func TestDispatch_AllowsDockerRequiredSessionWhenProviderSupported(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	environmentID := createTestEnvironmentWithDocker(ctx, t, pool)
	sessionID := createTestSessionWithEnvironment(ctx, t, pool, environmentID)

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	provider := &fakeSpawnProvider{
		nextRef:         ports.SandboxRef{ProviderID: "provider-docker-supported"},
		dockerSupported: true,
	}
	r := newDispatchTestRegistry(t, ctx, pool, provider, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	waitUntil(t, 5*time.Second, func() bool { return provider.callCount() == 1 })

	spec := provider.lastSpec()
	if !spec.Docker {
		t.Error("CreateSpec.Docker = false, want true")
	}
	if !spec.SessionConfig.Docker {
		t.Error("CreateSpec.SessionConfig.Docker = false, want true")
	}
}

// TestDispatch_RefusesEgressAllowlistSessionWhenProviderUnsupported
// mirrors the Docker pair above for §27.6's own egress half.
func TestDispatch_RefusesEgressAllowlistSessionWhenProviderUnsupported(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	environmentID := createTestEnvironmentWithEgressAllowlist(ctx, t, pool, []byte(`["registry.npmjs.org"]`))
	sessionID := createTestSessionWithEnvironment(ctx, t, pool, environmentID)

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	provider := &fakeSpawnProvider{
		nextRef:               ports.SandboxRef{ProviderID: "provider-should-never-be-called"},
		egressPolicySupported: false,
	}
	r := newDispatchTestRegistry(t, ctx, pool, provider, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	time.Sleep(300 * time.Millisecond)

	if got := provider.callCount(); got != 0 {
		t.Errorf("provider.callCount() = %d, want 0 -- CreateSandbox must never be called for an enforced-egress session against a provider that does not support it", got)
	}
}

// TestDispatch_AppendsAllowlistFloorEndToEnd proves the non-negotiable
// allowlist floor (Step 74 brief, point B) is genuinely present on the
// real wire CreateSpec a spawn sends, computed from THIS session's own
// actual repo, even though the customer's own configured allowlist named
// only an unrelated host.
func TestDispatch_AppendsAllowlistFloorEndToEnd(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	environmentID := createTestEnvironmentWithEgressAllowlist(ctx, t, pool, []byte(`["registry.npmjs.org"]`))

	created, err := narvipg.NewSessionStore(pool).Create(ctx, sqlcgen.CreateSessionParams{
		SpawnSource:   sqlcgen.SessionSpawnSourceWeb,
		EnvironmentID: environmentID,
		Repos:         []byte(`[{"name":"narvi","url":"https://github.com/khazaddev/narvi","branch":null}]`),
	})
	if err != nil {
		t.Fatalf("create test session: %v", err)
	}
	sessionID := created.ID

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	provider := &fakeSpawnProvider{
		nextRef:               ports.SandboxRef{ProviderID: "provider-egress-floor"},
		egressPolicySupported: true,
	}
	r := newDispatchTestRegistry(t, ctx, pool, provider, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	waitUntil(t, 5*time.Second, func() bool { return provider.callCount() == 1 })

	spec := provider.lastSpec()
	if spec.SessionConfig.EgressPolicy == nil {
		t.Fatal("SessionConfig.EgressPolicy = nil, want a non-nil allowlist policy")
	}
	allowlist := spec.SessionConfig.EgressPolicy.Allowlist

	foundCustomer, foundGitHost := false, false
	for _, host := range allowlist {
		if host == "registry.npmjs.org" {
			foundCustomer = true
		}
		if host == "github.com" {
			foundGitHost = true
		}
	}
	if !foundCustomer {
		t.Errorf("allowlist %v does not contain the customer's own configured host registry.npmjs.org", allowlist)
	}
	if !foundGitHost {
		t.Errorf("allowlist %v does not contain this session's own actual git host github.com (the non-negotiable floor)", allowlist)
	}
}
