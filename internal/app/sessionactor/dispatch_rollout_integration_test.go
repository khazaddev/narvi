//go:build integration

// This file proves Step 76's own dispatch-time half of the "fail-closed,
// twice" rule (§10 Phase 6, §32): tryPlanSpawn's own refuseIfRolloutUnenrolled
// (dispatch.go) refuses to spawn a session whose own named repo is not
// enrolled under rollout.ModeCohort -- mirrors dispatch_substrate_
// integration_test.go's own identical structure/reasoning exactly, one
// Step later, for the SAME "fail-closed, twice" family of guards. This is
// what makes §32's own rollback bound real: a session's Environment/repos
// are set DIRECTLY against Postgres here (createTestSessionWithRepos,
// pushpr_integration_test.go), never through httpapi.CreateSessionOnTx's
// own creation-time gate -- proving the dispatch-time check is a
// genuinely independent second guard, not merely a second call into logic
// the creation-time gate already exercised (the exact scenario a
// de-enrolled repo's own already-existing PR review session is in, every
// time a later @mention/re-review re-dispatches it).
package sessionactor

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/domain/rollout"
	"github.com/khazaddev/narvi/internal/platform"
)

// newDispatchTestRegistryWithRolloutMode mirrors newDispatchTestRegistry
// (dispatch_integration_test.go) exactly, with ONE addition:
// RegistryOptions.RolloutMode set to the caller's own value -- every
// OTHER newDispatchTestRegistry caller in this package implicitly gets
// rollout.ModeOpen (RegistryOptions' own zero value), proven not to
// change any of their behavior by this package's own pre-existing,
// untouched dispatch tests continuing to pass unmodified.
func newDispatchTestRegistryWithRolloutMode(t *testing.T, ctx context.Context, pool *pgxpool.Pool, provider ports.SandboxProvider, mode platform.RolloutMode) *Registry {
	t.Helper()
	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, provider, "http://localhost:8080", nil, nil, "", nil, false,
		RegistryOptions{RolloutMode: mode})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return r
}

// TestDispatch_RefusesUnenrolledRepoUnderCohortMode is a MUTATION-TESTABLE
// guard (mirrors dispatch_substrate_integration_test.go's own identical
// "remove the dispatch re-check -> this test must fail" framing, Step 74
// brief, applied to Step 76's own dispatch-time gate): the session's own
// repo is never enrolled (no repo_settings row written at all) and
// rollout.ModeCohort is armed -- CreateSandbox must never be called.
func TestDispatch_RefusesUnenrolledRepoUnderCohortMode(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	repoURL := "https://github.com/acme/" + t.Name() + ".git"
	sessionID := createTestSessionWithRepos(ctx, t, pool, pgtype.UUID{}, "widgets", repoURL, "")

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	provider := &fakeSpawnProvider{
		nextRef: ports.SandboxRef{ProviderID: "provider-should-never-be-called"},
	}
	r := newDispatchTestRegistryWithRolloutMode(t, ctx, pool, provider, rollout.ModeCohort)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	// Generous, fixed window -- mirrors dispatch_substrate_integration_
	// test.go's own identical "prove a negative" precedent.
	time.Sleep(300 * time.Millisecond)

	if got := provider.callCount(); got != 0 {
		t.Errorf("provider.callCount() = %d, want 0 -- CreateSandbox must never be called for a session whose repo is not enrolled under cohort mode", got)
	}

	sandboxes := narvipg.NewSandboxStore(pool)
	if _, err := sandboxes.Get(ctx, sessionID); err == nil {
		t.Error("sandboxes.Get succeeded -- want no sandbox row at all (the refusal must run before ANY spawn-claim write)")
	}
}

// TestDispatch_AllowsEnrolledRepoUnderCohortMode is the refusal test's own
// positive control: the IDENTICAL session/repo, but repo_settings.
// sessions_enabled is true for it -- proves the dispatch-time gate is a
// real, bidirectional decision, not something that happens to always
// refuse.
func TestDispatch_AllowsEnrolledRepoUnderCohortMode(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	repoFullName := "acme/" + t.Name()
	repoURL := "https://github.com/" + repoFullName + ".git"
	sessionID := createTestSessionWithRepos(ctx, t, pool, pgtype.UUID{}, "widgets", repoURL, "")

	repoSettings := narvipg.NewRepoSettingsStore(pool)
	if _, err := repoSettings.UpsertSessionsEnabled(ctx, repoFullName, true); err != nil {
		t.Fatalf("seed enrollment: %v", err)
	}

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	provider := &fakeSpawnProvider{
		nextRef: ports.SandboxRef{ProviderID: "provider-enrolled-repo"},
	}
	r := newDispatchTestRegistryWithRolloutMode(t, ctx, pool, provider, rollout.ModeCohort)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	waitUntil(t, 2*time.Second, func() bool { return provider.callCount() > 0 })

	if got := provider.callCount(); got != 1 {
		t.Errorf("provider.callCount() = %d, want 1 -- an enrolled repo must spawn normally", got)
	}
}

// TestDispatch_RefusesDeEnrolledRepoOnRespawn is §32's own documented
// rollback scenario, exercised directly: a session that WAS enrolled
// (and would have spawned) has its own repo's sessions_enabled flipped
// back to false, mid-session -- exactly what an operator's own rollback
// write does (§32.8/§32.9). The NEXT dispatch attempt against it must
// refuse, proving de-enrollment stops future respawns of an
// already-existing session, not just brand-new creates.
func TestDispatch_RefusesDeEnrolledRepoOnRespawn(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	repoFullName := "acme/" + t.Name()
	repoURL := "https://github.com/" + repoFullName + ".git"
	sessionID := createTestSessionWithRepos(ctx, t, pool, pgtype.UUID{}, "widgets", repoURL, "")

	repoSettings := narvipg.NewRepoSettingsStore(pool)
	// Enrolled at first...
	if _, err := repoSettings.UpsertSessionsEnabled(ctx, repoFullName, true); err != nil {
		t.Fatalf("seed enrollment: %v", err)
	}
	// ...then an operator's own rollback write de-enrolls it, BEFORE this
	// session's own first dispatch attempt ever runs (simulating a
	// rollback that lands between a REUSE-branch turn enqueue and this
	// Actor's own dispatch).
	if _, err := repoSettings.UpsertSessionsEnabled(ctx, repoFullName, false); err != nil {
		t.Fatalf("seed de-enrollment: %v", err)
	}

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "re-review after new commits")

	provider := &fakeSpawnProvider{
		nextRef: ports.SandboxRef{ProviderID: "provider-should-never-be-called"},
	}
	r := newDispatchTestRegistryWithRolloutMode(t, ctx, pool, provider, rollout.ModeCohort)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	time.Sleep(300 * time.Millisecond)

	if got := provider.callCount(); got != 0 {
		t.Errorf("provider.callCount() = %d, want 0 -- a de-enrolled repo's session must never respawn, even though it was enrolled when its turn was enqueued", got)
	}
}

// TestDispatch_OpenMode_SpawnsRegardlessOfEnrollment is the no-op proof
// at the dispatch layer (§32's own "byte-for-byte no-op" property,
// mirrored from the creation-time gate's own identical proof,
// rolloutgate_integration_test.go in package httpapi): rollout.ModeOpen
// (RegistryOptions' own zero value, and platform.Load's own default when
// NARVI_ROLLOUT_MODE is unset) spawns normally even though the session's
// own repo has NO repo_settings row at all.
func TestDispatch_OpenMode_SpawnsRegardlessOfEnrollment(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	repoURL := "https://github.com/acme/" + t.Name() + ".git"
	sessionID := createTestSessionWithRepos(ctx, t, pool, pgtype.UUID{}, "widgets", repoURL, "")

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	provider := &fakeSpawnProvider{
		nextRef: ports.SandboxRef{ProviderID: "provider-open-mode"},
	}
	// rollout.ModeOpen explicitly, though it is also RegistryOptions'
	// own zero value -- named here for clarity, not relying on the zero
	// value silently.
	r := newDispatchTestRegistryWithRolloutMode(t, ctx, pool, provider, rollout.ModeOpen)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	waitUntil(t, 2*time.Second, func() bool { return provider.callCount() > 0 })

	if got := provider.callCount(); got != 1 {
		t.Errorf("provider.callCount() = %d, want 1 -- open mode must spawn regardless of enrollment", got)
	}
}
