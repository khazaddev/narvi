//go:build integration

package sessionactor

import (
	"context"
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
// end-to-end wiring: dispatch.go/imageresolve.go's resolveAndSetImage on
// the spawn side, and internal/app/imagebuild.Builder on the background
// side, against a REAL Postgres instance -- see that package's own doc.go
// and this file's own top comment on dispatch_integration_test.go's
// fakeSpawnProvider for how BuildImage is faked.

// testRuntimeVersion is a fixed, obviously-fake runtime version used only
// by this file's own tests -- never platform.DefaultTimeouts()'s real
// default, so a fingerprint computed here can never collide with one
// computed against a real config by accident.
const testRuntimeVersion = "1.0.0-test"

// newImageBuildTestRegistry builds a Registry wired with everything Step
// 26's own image-resolution path reads: provider (for CreateSandbox/
// BuildImage), sourceControl (for ResolveBranchSHA), testTokenEncryptionKey
// (pushpr_integration_test.go's own fixed test key), and testRuntimeVersion.
func newImageBuildTestRegistry(t *testing.T, ctx context.Context, pool *pgxpool.Pool, provider ports.SandboxProvider, sourceControl ports.SourceControl) *Registry {
	t.Helper()
	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, provider, "http://localhost:8080",
		sourceControl, testTokenEncryptionKey, testRuntimeVersion)
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
// image_builds row is created for its fingerprint so
// internal/app/imagebuild's own background loop can pick it up later.
func TestResolveAndSetImage_NoCachedImage_FallsBackToBaseAndCreatesPendingRow(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	creator := createTestUserWithGitHubToken(ctx, t, pool, "gh-fake-token-a")
	sessionID := createTestSessionWithRepos(ctx, t, pool, creator,
		"repo1", "https://github.com/acme/repo1.git", "main")

	sourceControl := &fakeSourceControl{nextSHA: "sha-scenario-a"}
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

	if sourceControl.shaCallCount() != 1 {
		t.Fatalf("ResolveBranchSHA call count = %d, want 1", sourceControl.shaCallCount())
	}

	wantFingerprint := domainimagebuild.Fingerprint(defaultBaseImage, map[string]string{"repo1": "sha-scenario-a"}, testRuntimeVersion)

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
}

// TestResolveAndSetImage_NoUsableGitHubToken_StillSpawnsOnBaseImage proves
// scenario (d): a session whose creator has no usable GitHub token (here:
// no created_by user at all, the simplest of the several ways
// decryptCreatorGitHubToken reports "no usable credential") still spawns
// successfully on the base image -- never blocked or failed by this
// mechanism. ResolveBranchSHA is never even attempted.
func TestResolveAndSetImage_NoUsableGitHubToken_StillSpawnsOnBaseImage(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	sessionID := createTestSessionWithRepos(ctx, t, pool, pgtype.UUID{}, // no created_by
		"repo1", "https://github.com/acme/repo1.git", "main")

	sourceControl := &fakeSourceControl{nextSHA: "sha-should-never-be-used"}
	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-d"}}
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
		t.Errorf("CreateSpec.Image = %q, want the base image %q (no usable token -> never blocked, falls back)", spec.Image, defaultBaseImage)
	}

	if sourceControl.shaCallCount() != 0 {
		t.Errorf("ResolveBranchSHA call count = %d, want 0 (should never be attempted with no usable token)", sourceControl.shaCallCount())
	}

	sandboxStore := narvipg.NewSandboxStore(pool)
	waitUntil(t, 5*time.Second, func() bool {
		row, err := sandboxStore.Get(ctx, sessionID)
		return err == nil && sqlcgen.SandboxStatus(row.Status) == sqlcgen.SandboxStatusConnecting
	})
}

// --- Creator disabled/role recheck (audit finding, cross-step: this
// package's own CheckCreatorGuard, githubtoken.go) ---

// TestResolveAndSetImage_DisabledCreator_FallsBackToBaseImage proves a
// session whose creator was disabled AFTER session creation -- mid-
// session, e.g. an admin's own offboarding or incident-response disable --
// still spawns successfully, on the base image, WITHOUT ever attempting
// ResolveBranchSHA, even though the creator has an otherwise-real, usable,
// encrypted GitHub identity/token (proving this is SPECIFICALLY the new
// CheckCreatorGuard recheck, not an incidental no-usable-token skip like
// TestResolveAndSetImage_NoUsableGitHubToken_StillSpawnsOnBaseImage
// above). Mirrors internal/adapters/inbound/httpapi's own
// TestScmCredentials_DisabledCreator_Denied and this package's own
// TestHandleSandboxEvent_PushComplete_DisabledCreator_SkipsPRCreation
// (pushpr_integration_test.go): same staleness scenario, same session
// creator, just exercised at THIS call site -- the gap this batch's own
// audit sweep found left open here.
func TestResolveAndSetImage_DisabledCreator_FallsBackToBaseImage(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	creator := createTestUserWithGitHubToken(ctx, t, pool, "gh-fake-token-disabled-image")
	sessionID := createTestSessionWithRepos(ctx, t, pool, creator,
		"repo-disabled", "https://github.com/acme/repo-disabled.git", "main")

	// Disable the session creator AFTER the session already exists --
	// mirrors pushpr_integration_test.go's own established precedent (no
	// UserStore mutation exists for Disabled today, only ListMembers' own
	// read exposure, httpapi/members.go).
	if _, err := pool.Exec(ctx, `UPDATE users SET disabled = true WHERE id = $1`, creator); err != nil {
		t.Fatalf("disable fixture user: %v", err)
	}

	sourceControl := &fakeSourceControl{nextSHA: "sha-should-never-be-used"}
	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-disabled-image"}}
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
		t.Errorf("CreateSpec.Image = %q, want the base image %q (session creator is disabled)", spec.Image, defaultBaseImage)
	}
	if got := sourceControl.shaCallCount(); got != 0 {
		t.Errorf("ResolveBranchSHA call count = %d, want 0 (session creator is disabled)", got)
	}

	sandboxStore := narvipg.NewSandboxStore(pool)
	waitUntil(t, 5*time.Second, func() bool {
		row, getErr := sandboxStore.Get(ctx, sessionID)
		return getErr == nil && sqlcgen.SandboxStatus(row.Status) == sqlcgen.SandboxStatusConnecting
	})
}

// TestResolveAndSetImage_DemotedToViewerCreator_FallsBackToBaseImage is the
// same proof as TestResolveAndSetImage_DisabledCreator_FallsBackToBaseImage
// above, for the OTHER half of CheckCreatorGuard's own §13.3 viewer-guard
// threshold: a creator demoted to viewer (rather than disabled) AFTER
// session creation. Uses a real UserStore.UpdateRole call (the same
// mutation an admin's own real role-change endpoint performs), not raw
// SQL, since that store method already exists -- mirrors
// scmcredentials_integration_test.go's own
// TestScmCredentials_DemotedToViewerCreator_Denied precedent exactly.
func TestResolveAndSetImage_DemotedToViewerCreator_FallsBackToBaseImage(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	creator := createTestUserWithGitHubToken(ctx, t, pool, "gh-fake-token-viewer-image")
	sessionID := createTestSessionWithRepos(ctx, t, pool, creator,
		"repo-viewer", "https://github.com/acme/repo-viewer.git", "main")

	if _, err := narvipg.NewUserStore(pool).UpdateRole(ctx, creator, sqlcgen.UserRoleViewer); err != nil {
		t.Fatalf("demote fixture user to viewer: %v", err)
	}

	sourceControl := &fakeSourceControl{nextSHA: "sha-should-never-be-used"}
	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-viewer-image"}}
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
		t.Errorf("CreateSpec.Image = %q, want the base image %q (session creator is now a viewer)", spec.Image, defaultBaseImage)
	}
	if got := sourceControl.shaCallCount(); got != 0 {
		t.Errorf("ResolveBranchSHA call count = %d, want 0 (session creator is now a viewer)", got)
	}

	sandboxStore := narvipg.NewSandboxStore(pool)
	waitUntil(t, 5*time.Second, func() bool {
		row, getErr := sandboxStore.Get(ctx, sessionID)
		return getErr == nil && sqlcgen.SandboxStatus(row.Status) == sqlcgen.SandboxStatusConnecting
	})
}

// TestImageBuildPipeline_BackgroundBuilderPicksUpPendingRow_LaterSpawnGetsRealImage
// proves scenario (b): the FULL pipeline, spanning both dispatch.go's own
// spawn-time resolution and internal/app/imagebuild.Builder's own
// background loop, against the SAME real Postgres pool and the SAME fake
// provider instance:
//
//  1. session1's own first spawn has no cached image yet -> base image,
//     pending row created (exactly scenario (a) above).
//  2. Builder.PumpOnce claims that pending row, calls the fake provider's
//     own (now configurable) BuildImage, and records success.
//  3. session2 -- a DIFFERENT session naming the SAME repo/branch (so
//     ResolveBranchSHA resolves to the identical sha, and therefore the
//     identical fingerprint) -- spawns and gets the REAL built image_ref,
//     not the base image, with BootMode upgraded to RepoImage.
func TestImageBuildPipeline_BackgroundBuilderPicksUpPendingRow_LaterSpawnGetsRealImage(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	creator := createTestUserWithGitHubToken(ctx, t, pool, "gh-fake-token-b")

	sourceControl := &fakeSourceControl{nextSHA: "sha-scenario-b"}
	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-b1"}}
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

	wantFingerprint := domainimagebuild.Fingerprint(defaultBaseImage, map[string]string{"repo1": "sha-scenario-b"}, testRuntimeVersion)
	imageBuildStore := narvipg.NewImageBuildStore(pool)
	waitUntil(t, 5*time.Second, func() bool {
		_, err := imageBuildStore.Get(ctx, wantFingerprint)
		return err == nil
	})

	// Step 2: the background builder claims and builds it.
	const wantImageRef = "narvi/built-image:scenario-b"
	provider.nextBuildRef = ports.BuildRef(wantImageRef)

	builder, err := imagebuild.NewBuilder(imageBuildStore, pool, provider, platform.DefaultTimeouts())
	if err != nil {
		t.Fatalf("NewBuilder: %v", err)
	}
	if err := builder.PumpOnce(ctx); err != nil {
		t.Fatalf("PumpOnce: %v", err)
	}

	if provider.buildCallCount() != 1 {
		t.Fatalf("BuildImage call count = %d, want 1", provider.buildCallCount())
	}
	buildSpec := provider.buildCalls[0]
	if buildSpec.Base != defaultBaseImage {
		t.Errorf("BuildImage ImageSpec.Base = %q, want %q", buildSpec.Base, defaultBaseImage)
	}
	if buildSpec.RepoSHAs["repo1"] != "sha-scenario-b" {
		t.Errorf("BuildImage ImageSpec.RepoSHAs[repo1] = %q, want %q", buildSpec.RepoSHAs["repo1"], "sha-scenario-b")
	}
	if buildSpec.RuntimeVersion != testRuntimeVersion {
		t.Errorf("BuildImage ImageSpec.RuntimeVersion = %q, want %q", buildSpec.RuntimeVersion, testRuntimeVersion)
	}

	row, err := imageBuildStore.Get(ctx, wantFingerprint)
	if err != nil {
		t.Fatalf("get image_builds row after build: %v", err)
	}
	if row.Status != sqlcgen.ImageBuildStatusReady {
		t.Fatalf("image_builds.status = %q, want %q", row.Status, sqlcgen.ImageBuildStatusReady)
	}
	if row.ImageRef == nil || *row.ImageRef != wantImageRef {
		t.Fatalf("image_builds.image_ref = %v, want %q", row.ImageRef, wantImageRef)
	}

	// Step 3: session2, same repo/branch (same fingerprint), spawns and
	// gets the REAL built image, not the base image.
	session2 := createTestSessionWithRepos(ctx, t, pool, creator,
		"repo1", "https://github.com/acme/repo1.git", "main")
	createPendingTurn(ctx, t, turnStore, session2, "prompt 2")

	provider2 := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-b2"}}
	r2 := newImageBuildTestRegistry(t, ctx, pool, provider2, sourceControl)
	t.Cleanup(func() { _ = r2.Shutdown() })

	a2, err := r2.GetOrSpawn(ctx, session2)
	if err != nil {
		t.Fatalf("GetOrSpawn(session2): %v", err)
	}
	sendEnsureDispatched(ctx, t, a2)
	waitUntil(t, 5*time.Second, func() bool { return provider2.callCount() == 1 })

	spec2 := provider2.lastSpec()
	if spec2.Image != wantImageRef {
		t.Errorf("session2 CreateSpec.Image = %q, want the real built image %q, not the base image", spec2.Image, wantImageRef)
	}
	if spec2.SessionConfig.BootMode != sessionconfig.SessionConfigBootModeRepoImage {
		t.Errorf("session2 SessionConfig.BootMode = %q, want %q (a real prebuilt image was found)",
			spec2.SessionConfig.BootMode, sessionconfig.SessionConfigBootModeRepoImage)
	}
}
