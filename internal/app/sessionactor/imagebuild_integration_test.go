//go:build integration

package sessionactor

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/imagebuild"
	"github.com/khazaddev/narvi/internal/app/ports"
	domainimagebuild "github.com/khazaddev/narvi/internal/domain/imagebuild"
	"github.com/khazaddev/narvi/internal/platform"
)

// This file proves Step 26's ("image builds", §8.5-note/§10-P2) own
// end-to-end wiring, as rewritten by Step 41 ("warm boot: shared
// fingerprint + spawn-path simplification", §19.1): dispatch.go/
// imageresolve.go's resolveAndSetImage on the spawn side, and internal/
// app/imagebuild.Builder on the background side, against a REAL Postgres
// instance -- see that package's own doc.go and this file's own top
// comment on dispatch_integration_test.go's fakeSpawnProvider for how
// BuildImage is faked.
//
// # Step 41/42 boundary this file's own tests are written against
//
// resolveAndSetImage no longer calls ResolveBranchSHA, CheckCreatorGuard,
// or decryptCreatorGitHubToken at all (imageresolve.go's own top comment)
// -- the fingerprint is computed purely from plan.spec.SessionConfig.
// Repos' own (name, url) pairs, zero network calls, on every spawn. Every
// test below that exercises a cache MISS or a cache HIT asserts
// sourceControl.shaCallCount() == 0 for exactly this reason: there is no
// code path left in this Step that could ever make that count anything
// else. Separately, app/imagebuild.Builder's own attempt has no
// claim-time SHA resolution mechanism yet (that's §19.2/§19.9),
// so a background builder can only ever turn a REPO-LESS pending row into
// a real 'ready' one in Step 41 -- a repo-bearing pending row this file's
// own MISS tests create stays unresolved (see
// TestImageBuildPipeline_MissCreatesPendingRow_BuilderCannotYetBuildIt_
// SpawnStillBaseImage below, and internal/app/imagebuild/
// builder_integration_test.go's own dedicated coverage of that skip path
// in isolation). The WARM-HIT test below therefore seeds a 'ready' row
// directly (via ImageBuildStore, bypassing the background builder
// entirely) rather than relying on the builder to produce one for a
// repo-bearing fingerprint -- simulating what Step 42's own claim-time
// resolution will eventually produce for real, so this Step's own exit
// criterion ("existing spawn-path behavior, the warm-hit case, must work
// end-to-end") has real, direct coverage today.

// testRuntimeVersion is a fixed, obviously-fake runtime version used only
// by this file's own tests -- never platform.DefaultTimeouts()'s real
// default, so a fingerprint computed here can never collide with one
// computed against a real config by accident.
const testRuntimeVersion = "1.0.0-test"

// newImageBuildTestRegistry builds a Registry wired with everything Step
// 26's own image-resolution path reads: provider (for CreateSandbox/
// BuildImage), sourceControl (kept for signature parity / other Actor
// functionality -- imageresolve.go itself never calls it as of Step 41,
// see this file's own top comment), testTokenEncryptionKey
// (pushpr_integration_test.go's own fixed test key), and testRuntimeVersion.
func newImageBuildTestRegistry(t *testing.T, ctx context.Context, pool *pgxpool.Pool, provider ports.SandboxProvider, sourceControl ports.SourceControl) *Registry {
	t.Helper()
	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, provider, "http://localhost:8080",
		sourceControl, testTokenEncryptionKey, testRuntimeVersion, nil, false)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return r
}

// createTestUserWithGitHubToken creates a real user + a real (encrypted)
// GitHub identity carrying plaintextToken, mirroring
// TestHandleSandboxEvent_PushComplete_CreatesPRArtifact's own inline setup
// exactly (pushpr_integration_test.go) -- factored into a shared helper
// here since this file's own tests need the identical setup more than
// once.
func createTestUserWithGitHubToken(ctx context.Context, t *testing.T, pool *pgxpool.Pool, plaintextToken string) pgtype.UUID {
	t.Helper()

	userStore := narvipg.NewUserStore(pool)
	identityStore := narvipg.NewIdentityStore(pool)

	user, err := userStore.Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: fmt.Sprintf("imagebuild-test-%d@example.com", time.Now().UnixNano()),
		DisplayName:  "Image Build Test User",
		Role:         sqlcgen.UserRoleMember,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	encrypted, err := platform.EncryptToken(testTokenEncryptionKey, []byte(plaintextToken))
	if err != nil {
		t.Fatalf("EncryptToken: %v", err)
	}
	email := user.PrimaryEmail
	if _, err := identityStore.Create(ctx, sqlcgen.CreateIdentityParams{
		UserID:               user.ID,
		Provider:             sqlcgen.IdentityProviderGithub,
		ExternalID:           fmt.Sprintf("imagebuild-test-external-%d", time.Now().UnixNano()),
		Email:                &email,
		EmailVerified:        true,
		LinkedVia:            sqlcgen.IdentityLinkedViaAdmin,
		AccessTokenEncrypted: encrypted,
	}); err != nil {
		t.Fatalf("create identity: %v", err)
	}

	return user.ID
}

// TestResolveAndSetImage_NoCachedImage_FallsBackToBaseAndCreatesPendingRow
// proves scenario (a): a session with real repos and no cached image yet
// gets the base image for its own current spawn (§10 Phase 2's own
// "always fall back to base image on any miss"), AND a pending
// image_builds row is created for its fingerprint -- carrying the
// NORMALIZED repo url, never a resolved sha (§19.1) -- so
// internal/app/imagebuild's own background loop has a record of this repo
// set. ResolveBranchSHA is never called at all -- the fingerprint is
// computed purely from the session's own configured repo URL.
func TestResolveAndSetImage_NoCachedImage_FallsBackToBaseAndCreatesPendingRow(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	creator := createTestUserWithGitHubToken(ctx, t, pool, "gh-fake-token-a")
	sessionID := createTestSessionWithRepos(ctx, t, pool, creator,
		"repo1", "https://github.com/acme/repo1.git", "main")

	sourceControl := &fakeSourceControl{nextSHA: "sha-should-never-be-used"}
	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-a"}}
	r := newImageBuildTestRegistry(t, ctx, pool, provider, sourceControl)
	t.Cleanup(func() { _ = r.Shutdown() })

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	waitUntil(t, 5*time.Second, func() bool { return provider.callCount() == 1 })

	spec := provider.lastSpec()
	if spec.Image != defaultBaseImage {
		t.Errorf("CreateSpec.Image = %q, want the base image %q (no cached image exists yet)", spec.Image, defaultBaseImage)
	}

	if got := sourceControl.shaCallCount(); got != 0 {
		t.Fatalf("ResolveBranchSHA call count = %d, want 0 (Step 41: the fingerprint is url-keyed and network-free)", got)
	}

	// The fingerprint is computed from the repo's clone URL directly --
	// Fingerprint itself normalizes (NormalizeRepoURL), so passing the
	// raw configured (".git"-suffixed) URL here matches what
	// imageresolve.go computed from the identical raw config.
	wantFingerprint := domainimagebuild.Fingerprint(defaultBaseImage, map[string]string{"repo1": "https://github.com/acme/repo1.git"}, testRuntimeVersion)

	imageBuildStore := narvipg.NewImageBuildStore(pool)
	var row sqlcgen.ImageBuild
	waitUntil(t, 5*time.Second, func() bool {
		row, err = imageBuildStore.Get(ctx, wantFingerprint)
		return err == nil
	})

	if row.Status != sqlcgen.ImageBuildStatusPending {
		t.Errorf("image_builds.status = %q, want %q", row.Status, sqlcgen.ImageBuildStatusPending)
	}
	if row.Base != defaultBaseImage {
		t.Errorf("image_builds.base = %q, want %q", row.Base, defaultBaseImage)
	}
	if row.RuntimeVersion != testRuntimeVersion {
		t.Errorf("image_builds.runtime_version = %q, want %q", row.RuntimeVersion, testRuntimeVersion)
	}
	if row.ImageRef != nil {
		t.Errorf("image_builds.image_ref = %v, want nil (not built yet)", row.ImageRef)
	}
	if row.AttemptCount != 0 {
		t.Errorf("image_builds.attempt_count = %d, want 0 (never claimed/attempted yet)", row.AttemptCount)
	}
	if row.BuiltRepoShas != nil {
		t.Errorf("image_builds.built_repo_shas = %v, want nil (never built)", row.BuiltRepoShas)
	}

	var gotRepoURLs map[string]string
	if err := json.Unmarshal(row.RepoUrls, &gotRepoURLs); err != nil {
		t.Fatalf("unmarshal repo_urls: %v", err)
	}
	wantRepoURLs := map[string]string{"repo1": "https://github.com/acme/repo1"} // .git suffix normalized away
	if gotRepoURLs["repo1"] != wantRepoURLs["repo1"] {
		t.Errorf("image_builds.repo_urls[repo1] = %q, want %q (normalized, .git suffix stripped)", gotRepoURLs["repo1"], wantRepoURLs["repo1"])
	}
}

// TestResolveAndSetImage_CreatorContextIrrelevant_ZeroNetworkCallsRegardless
// (Step 41's own test proving creator context -- no account, disabled,
// viewer -- never changed resolveAndSetImage's outcome) has been REMOVED
// by the audit fix ("warm-boot image access control", HIGH): that was
// exactly the vulnerability this batch closes -- see the finding this
// batch's own PR description cites, and imageresolve.go's own new
// "# Repo-access gate" top comment. Creator context is no longer
// irrelevant; it is now the FIRST thing checked, and denies warm-boot
// outright for every one of those three cases. The replacement coverage
// lives in this package's own repoaccessgate_integration_test.go:
// TestRepoAccessGate_NoCreatedByUser_DeniesWarmBoot (no created_by) and
// TestRepoAccessGate_DisabledOrViewerCreator_DeniesWarmBootNoAccessCallEither
// (disabled/viewer, table-driven, mirroring this test's own former shape).

// TestResolveAndSetImage_WarmHit_UsesReadyImageZeroNetworkCalls proves this
// Step's own exit criterion: existing spawn-path behavior for the
// warm-HIT case (a fingerprint that already has a 'ready' image_builds
// row) works end to end, with ZERO network calls, exactly like before
// Step 41 -- only how the fingerprint itself got computed changed. The
// 'ready' row is seeded directly here (Claim + RecordSuccess against a
// pending row this test creates), simulating what Step 42's own
// claim-time resolution will eventually produce for a repo-bearing
// fingerprint for real -- Step 41's own background builder cannot produce
// one for a repo-bearing row itself yet (see this file's own top comment).
func TestResolveAndSetImage_WarmHit_UsesReadyImageZeroNetworkCalls(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	creator := createTestUserWithGitHubToken(ctx, t, pool, "gh-fake-token-warmhit")
	sessionID := createTestSessionWithRepos(ctx, t, pool, creator,
		"repo1", "https://github.com/acme/repo1.git", "main")

	fingerprint := domainimagebuild.Fingerprint(defaultBaseImage, map[string]string{"repo1": "https://github.com/acme/repo1.git"}, testRuntimeVersion)

	imageBuildStore := narvipg.NewImageBuildStore(pool)
	seedReadyImageBuild(ctx, t, imageBuildStore, fingerprint, "narvi/built-image:warm-hit")

	sourceControl := &fakeSourceControl{nextSHA: "sha-should-never-be-used"}
	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-warm-hit"}}
	r := newImageBuildTestRegistry(t, ctx, pool, provider, sourceControl)
	t.Cleanup(func() { _ = r.Shutdown() })

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	waitUntil(t, 5*time.Second, func() bool { return provider.callCount() == 1 })

	spec := provider.lastSpec()
	if spec.Image != "narvi/built-image:warm-hit" {
		t.Errorf("CreateSpec.Image = %q, want the real ready image %q", spec.Image, "narvi/built-image:warm-hit")
	}
	if spec.SessionConfig.BootMode != sessionconfig.SessionConfigBootModeRepoImage {
		t.Errorf("CreateSpec.SessionConfig.BootMode = %q, want %q (a real ready image was found)",
			spec.SessionConfig.BootMode, sessionconfig.SessionConfigBootModeRepoImage)
	}
	if got := sourceControl.shaCallCount(); got != 0 {
		t.Errorf("ResolveBranchSHA call count = %d, want 0 (the warm-hit path is network-free)", got)
	}
}

// seedReadyImageBuild drives a fingerprint's own image_builds row from
// nonexistent through 'pending' -> 'building' -> 'ready' (UpsertPending,
// Claim, RecordSuccess), landing exactly the shape a real successful
// build leaves behind -- used by tests that need a warm-HIT row to
// already exist without depending on app/imagebuild.Builder actually
// being able to produce one for a repo-bearing fingerprint (which it
// cannot yet, in Step 41 -- see this file's own top comment).
func seedReadyImageBuild(ctx context.Context, t *testing.T, store *narvipg.ImageBuildStore, fingerprint, imageRef string) {
	t.Helper()

	repoURLs, err := json.Marshal(map[string]string{"repo1": "https://github.com/acme/repo1"})
	if err != nil {
		t.Fatalf("marshal repo urls: %v", err)
	}
	if err := store.UpsertPending(ctx, sqlcgen.UpsertPendingImageBuildParams{
		Fingerprint:    fingerprint,
		Base:           defaultBaseImage,
		RepoUrls:       repoURLs,
		RuntimeVersion: testRuntimeVersion,
	}); err != nil {
		t.Fatalf("seed pending image_builds row: %v", err)
	}
	if _, err := store.Claim(ctx, fingerprint); err != nil {
		t.Fatalf("claim image_builds row: %v", err)
	}
	builtRepoSHAs, err := json.Marshal(map[string]string{"repo1": "sha-warm-hit"})
	if err != nil {
		t.Fatalf("marshal built repo shas: %v", err)
	}
	if _, err := store.RecordSuccess(ctx, sqlcgen.RecordImageBuildSuccessParams{
		Fingerprint:   fingerprint,
		ImageRef:      &imageRef,
		BuiltRepoShas: builtRepoSHAs,
		BuiltAt:       pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err != nil {
		t.Fatalf("record image_builds success: %v", err)
	}
}

// TestImageBuildPipeline_MissCreatesPendingRow_NoCredentialConfigured_SpawnStillBaseImage
// proves end to end, from the spawn side, Step 42's own degrade path when
// the new platform-level GitHub credential (platform.Config.
// GitHubImageBuildToken) is NOT configured: a cache MISS creates a pending
// row (scenario (a), re-proved here as part of the full pipeline), the
// background builder claims it but -- correctly, per §19.2's own
// deliberately-optional credential design -- cannot resolve any repo's
// SHA without a configured credential, so it records a clean, retryable
// failure rather than building with an empty/zero SHA, and a LATER spawn
// for the identical repo set therefore STILL falls back to the base
// image, exactly as if no image_builds row existed. See internal/app/
// imagebuild/builder_integration_test.go's own
// TestPumpOnce_RepoBearingRow_CredentialConfigured_ResolvesSHAsAndBuilds
// for the companion, credential-CONFIGURED case (a real build actually
// happens) -- this file's own scope (see its top comment) is the
// spawn-side pipeline, not the builder's own internals in isolation.
func TestImageBuildPipeline_MissCreatesPendingRow_NoCredentialConfigured_SpawnStillBaseImage(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	creator := createTestUserWithGitHubToken(ctx, t, pool, "gh-fake-token-pipeline")

	sourceControl := &fakeSourceControl{nextSHA: "sha-should-never-be-used"}
	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-pipeline-1"}}
	r := newImageBuildTestRegistry(t, ctx, pool, provider, sourceControl)
	t.Cleanup(func() { _ = r.Shutdown() })

	turnStore := narvipg.NewTurnStore(pool)

	// Step 1: session1's first spawn -- no cached image yet.
	session1 := createTestSessionWithRepos(ctx, t, pool, creator,
		"repo1", "https://github.com/acme/repo1.git", "main")
	createPendingTurn(ctx, t, turnStore, session1, "prompt 1")

	a1, err := r.GetOrSpawn(ctx, session1)
	if err != nil {
		t.Fatalf("GetOrSpawn(session1): %v", err)
	}
	sendEnsureDispatched(ctx, t, a1)
	waitUntil(t, 5*time.Second, func() bool { return provider.callCount() == 1 })

	if got := provider.lastSpec().Image; got != defaultBaseImage {
		t.Fatalf("session1 CreateSpec.Image = %q, want base image %q", got, defaultBaseImage)
	}
	if got := sourceControl.shaCallCount(); got != 0 {
		t.Fatalf("ResolveBranchSHA call count = %d, want 0", got)
	}

	wantFingerprint := domainimagebuild.Fingerprint(defaultBaseImage, map[string]string{"repo1": "https://github.com/acme/repo1.git"}, testRuntimeVersion)
	imageBuildStore := narvipg.NewImageBuildStore(pool)
	waitUntil(t, 5*time.Second, func() bool {
		_, err := imageBuildStore.Get(ctx, wantFingerprint)
		return err == nil
	})

	// Step 2: the background builder claims it, but -- with NO platform
	// GitHub credential configured (nil sourceControl, empty token) --
	// cannot resolve any repo's SHA, so BuildImage must never even be
	// called, and the row is recorded as a retryable failure rather than
	// getting stuck in 'building'.
	cacheVersionStore := narvipg.NewImageCacheVersionStore(pool)
	builder, err := imagebuild.NewBuilder(imageBuildStore, pool, provider, platform.DefaultTimeouts(), nil, "", cacheVersionStore)
	if err != nil {
		t.Fatalf("NewBuilder: %v", err)
	}
	if err := builder.PumpOnce(ctx); err != nil {
		t.Fatalf("PumpOnce: %v", err)
	}

	if got := provider.buildCallCount(); got != 0 {
		t.Fatalf("BuildImage call count = %d, want 0 (no platform credential configured -- degrade cleanly, §19.2)", got)
	}

	row, err := imageBuildStore.Get(ctx, wantFingerprint)
	if err != nil {
		t.Fatalf("get image_builds row after PumpOnce: %v", err)
	}
	if row.Status != sqlcgen.ImageBuildStatusFailed {
		t.Fatalf("image_builds.status = %q, want %q (retryable, not stuck 'building')", row.Status, sqlcgen.ImageBuildStatusFailed)
	}
	if row.ImageRef != nil {
		t.Fatalf("image_builds.image_ref = %v, want nil (never built)", row.ImageRef)
	}

	// Step 3: session2, same repo/branch (same fingerprint), spawns and
	// STILL gets the base image -- nothing was ever actually built.
	session2 := createTestSessionWithRepos(ctx, t, pool, creator,
		"repo1", "https://github.com/acme/repo1.git", "main")
	createPendingTurn(ctx, t, turnStore, session2, "prompt 2")

	provider2 := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-pipeline-2"}}
	r2 := newImageBuildTestRegistry(t, ctx, pool, provider2, sourceControl)
	t.Cleanup(func() { _ = r2.Shutdown() })

	a2, err := r2.GetOrSpawn(ctx, session2)
	if err != nil {
		t.Fatalf("GetOrSpawn(session2): %v", err)
	}
	sendEnsureDispatched(ctx, t, a2)
	waitUntil(t, 5*time.Second, func() bool { return provider2.callCount() == 1 })

	spec2 := provider2.lastSpec()
	if spec2.Image != defaultBaseImage {
		t.Errorf("session2 CreateSpec.Image = %q, want the base image %q (nothing was ever built)", spec2.Image, defaultBaseImage)
	}
	if spec2.SessionConfig.BootMode == sessionconfig.SessionConfigBootModeRepoImage {
		t.Errorf("session2 SessionConfig.BootMode = %q, want anything but %q (no real image was found)",
			spec2.SessionConfig.BootMode, sessionconfig.SessionConfigBootModeRepoImage)
	}
}
