//go:build integration

package sessionactor

import (
	"context"
	"testing"
	"time"

	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/app/imagebuild"
	"github.com/khazaddev/narvi/internal/app/ports"
	domainimagebuild "github.com/khazaddev/narvi/internal/domain/imagebuild"
	"github.com/khazaddev/narvi/internal/platform"
)

// TestResilienceScenario_RefreshInFlightSpawn_StillGetsOldReadyImage is
// this Step's own new §9.3-class resilience scenario (§19.2, Step 42's
// own "refresh-in-flight spawn" addition to the resilience suite): a NEW
// session spawn, targeting a fingerprint whose 'ready' image_builds row is
// GENUINELY mid-refresh (a real internal/app/imagebuild.Builder.
// RefreshOnce call, BuildImage blocked, not yet returned), must still get
// the OLD, already-ready image_ref -- never blocked, never falling back
// to the base image, exactly as §19.2's own "never degrades availability"
// guarantee requires.
//
// This drives the REAL spawn path end to end (a real sessionactor.Actor,
// resolveAndSetImage, a real Postgres image_builds row) concurrently with
// a REAL freshness-pump refresh attempt against the SAME row -- not a
// bare store-level read -- so this is the literal scenario as worded, not
// a unit test relabeled. internal/app/imagebuild/builder_integration_test.go's
// own TestRefreshOnce_OldRefStaysServableDuringRefresh proves the
// identical property at the store level in isolation; this test proves it
// through the full spawn path a real session actually takes.
func TestResilienceScenario_RefreshInFlightSpawn_StillGetsOldReadyImage(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	creator := createTestUserWithGitHubToken(ctx, t, pool, "gh-fake-token-refresh-inflight")
	repoName := "repo-refresh-inflight"
	repoURL := "https://github.com/acme/" + repoName + ".git"

	imageBuildStore := narvipg.NewImageBuildStore(pool)
	fingerprint := domainimagebuild.Fingerprint(defaultBaseImage, map[string]string{repoName: repoURL}, testRuntimeVersion)
	seedReadyImageBuild(ctx, t, imageBuildStore, fingerprint, "narvi/built-image:old-still-servable")

	// buildProvider is the imagebuild.Builder's own SandboxProvider --
	// BuildImage blocks (after recording the call) until the test releases
	// it, holding the refresh genuinely "in flight" for the whole window
	// this test observes the concurrent spawn's own outcome.
	release := make(chan struct{})
	buildProvider := &fakeSpawnProvider{
		nextBuildRef: "narvi/built-image:refreshed",
		buildBlock:   release,
	}
	sourceControl := &fakeSourceControl{shaFor: map[string]string{repoName: "sha-refresh-in-flight-new"}}

	cacheVersionStore := narvipg.NewImageCacheVersionStore(pool)
	builder, err := imagebuild.NewBuilder(imageBuildStore, pool, buildProvider, platform.DefaultTimeouts(),
		sourceControl, "test-platform-github-token", cacheVersionStore)
	if err != nil {
		t.Fatalf("NewBuilder: %v", err)
	}

	refreshDone := make(chan error, 1)
	go func() { refreshDone <- builder.RefreshOnce(ctx) }()

	waitUntil(t, 5*time.Second, func() bool { return buildProvider.buildCallCount() == 1 })

	// The refresh's own BuildImage call is now genuinely blocked, in
	// flight. A brand-new session, same repo/branch (same fingerprint),
	// spawns NOW -- through the real dispatch path, a SEPARATE provider
	// instance (spawning has nothing to do with the builder's own
	// provider) -- and must observe the STILL-ready row's OLD image_ref.
	spawnProvider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-refresh-inflight-spawn"}}
	spawnSourceControl := &fakeSourceControl{nextSHA: "sha-should-never-be-used"}
	r := newImageBuildTestRegistry(t, ctx, pool, spawnProvider, spawnSourceControl)
	t.Cleanup(func() { _ = r.Shutdown() })

	sessionID := createTestSessionWithRepos(ctx, t, pool, creator, repoName, repoURL, "main")
	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing while a refresh is in flight")

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	waitUntil(t, 5*time.Second, func() bool { return spawnProvider.callCount() == 1 })

	spec := spawnProvider.lastSpec()
	if spec.Image != "narvi/built-image:old-still-servable" {
		t.Errorf("CreateSpec.Image = %q, want the OLD, still-ready image %q (a refresh in flight must never degrade a concurrent spawn)",
			spec.Image, "narvi/built-image:old-still-servable")
	}
	if spec.SessionConfig.BootMode != sessionconfig.SessionConfigBootModeRepoImage {
		t.Errorf("CreateSpec.SessionConfig.BootMode = %q, want %q (the row is still genuinely ready)",
			spec.SessionConfig.BootMode, sessionconfig.SessionConfigBootModeRepoImage)
	}

	// Release the refresh and confirm it completes, swapping in the new
	// ref -- proving the earlier read really did observe a genuinely
	// in-flight, not-yet-committed refresh, not a already-finished one.
	close(release)
	if err := <-refreshDone; err != nil {
		t.Fatalf("RefreshOnce: %v", err)
	}

	row, err := imageBuildStore.Get(ctx, fingerprint)
	if err != nil {
		t.Fatalf("get row after refresh completed: %v", err)
	}
	if row.ImageRef == nil || *row.ImageRef != "narvi/built-image:refreshed" {
		t.Errorf("image_ref after refresh completed = %v, want the NEW ref %q", row.ImageRef, "narvi/built-image:refreshed")
	}
}
