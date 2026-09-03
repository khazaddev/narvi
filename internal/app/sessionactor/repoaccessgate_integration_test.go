//go:build integration

package sessionactor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/narvidev/narvi/contracts/gen/go/sessionconfig"
	narvipg "github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/app/ports"
	domainimagebuild "github.com/narvidev/narvi/internal/domain/imagebuild"
	"github.com/narvidev/narvi/internal/platform"
)

// This file proves the audit fix ("warm-boot image access control", HIGH)
// this batch closes end to end, from the spawn side, against a REAL
// Postgres instance -- see imageresolve.go's own "# Repo-access gate" top
// comment for the full design, and this package's own imagebuild_
// integration_test.go for the pre-existing miss/warm-hit pipeline coverage
// this fix's gate now sits in front of.

// noRowAppearsWindow is how long assertNoImageBuildRowAppears (below)
// keeps actively polling before it accepts "no row" as the final answer --
// test-adversarial audit fix (finding #13): a single fixed time.Sleep
// before ONE point-in-time check adds no real margin once the spawn's own
// synchronous chain has already been observed complete (waitUntil(provider.
// callCount()==1) already forces that), and erodes further under host
// contention. Actively polling across a MUCH longer window than the old
// 200ms (10x here) gives a future accidental async regression a real
// chance to be caught, while never slowing down the common (already-true)
// case: assertNoImageBuildRowAppears returns as soon as the window elapses
// without ever seeing a row, same as before, just with many samples
// instead of one.
const noRowAppearsWindow = 2 * time.Second

// assertNoImageBuildRowAppears polls store.Get(fingerprint) repeatedly
// across noRowAppearsWindow, failing the test immediately the first time a
// row is ever observed -- see noRowAppearsWindow's own doc comment for why
// this replaces a single fixed time.Sleep-then-check (test-adversarial
// audit fix, finding #13).
func assertNoImageBuildRowAppears(ctx context.Context, t *testing.T, store *narvipg.ImageBuildStore, fingerprint string) {
	t.Helper()

	deadline := time.Now().Add(noRowAppearsWindow)
	for {
		if _, err := store.Get(ctx, fingerprint); err == nil {
			t.Fatal("image_builds row exists, want none: a pending row must never be minted for a repo the requester cannot (or must not) read")
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestRepoAccessGate_NoAccess_CannotMintPendingRowNorWarmHit is the core
// security regression test: an authenticated member with a real, usable
// GitHub token that genuinely CANNOT read the named repo (CheckRepoAccess
// configured to deny it) must be denied BOTH of the two vectors the audit
// finding described --
//
//  1. minting a pending image_builds row for that repo at all (session1,
//     below): before this fix, resolveAndSetImage would best-effort
//     upsert a pending row for ANY caller-supplied repo URL, regardless of
//     access;
//  2. warm-hitting an already-'ready' row for that fingerprint that some
//     OTHER (legitimate) session's build already produced (session2,
//     below, against a row this test seeds directly, simulating that
//     other session): before this fix, any session naming the identical
//     repo set would silently inherit whichever image that fingerprint
//     already resolved to, with no access check of its own.
func TestRepoAccessGate_NoAccess_CannotMintPendingRowNorWarmHit(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	attacker := createTestUserWithGitHubToken(ctx, t, pool, "gh-fake-token-attacker")

	sourceControl := &fakeSourceControl{denyAllAccess: true}
	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-attacker-1"}}
	r := newImageBuildTestRegistry(t, ctx, pool, provider, sourceControl)
	t.Cleanup(func() { _ = r.Shutdown() })

	turnStore := narvipg.NewTurnStore(pool)
	imageBuildStore := narvipg.NewImageBuildStore(pool)

	// --- Vector 1: cannot mint a pending row. ---
	session1 := createTestSessionWithRepos(ctx, t, pool, attacker,
		"private-repo", "https://github.com/victim-org/private-repo.git", "main")
	createPendingTurn(ctx, t, turnStore, session1, "read the secrets")

	a1, err := r.GetOrSpawn(ctx, session1)
	if err != nil {
		t.Fatalf("GetOrSpawn(session1): %v", err)
	}
	sendEnsureDispatched(ctx, t, a1)
	waitUntil(t, 5*time.Second, func() bool { return provider.callCount() == 1 })

	if got := provider.lastSpec().Image; got != defaultBaseImage {
		t.Fatalf("session1 CreateSpec.Image = %q, want base image %q (no access -> no image, ever)", got, defaultBaseImage)
	}

	fingerprint := domainimagebuild.Fingerprint(defaultBaseImage,
		map[string]string{"private-repo": "https://github.com/victim-org/private-repo.git"}, testRuntimeVersion)

	// Give any (wrongly-firing) best-effort upsert a real, actively-polled
	// window it would need to land in, then assert it never did (test-
	// adversarial audit fix, finding #13 -- see assertNoImageBuildRowAppears's
	// own doc comment).
	assertNoImageBuildRowAppears(ctx, t, imageBuildStore, fingerprint)

	if got := sourceControl.accessCallCount(); got == 0 {
		t.Fatal("CheckRepoAccess call count = 0, want at least 1 (the gate must actually run, not merely happen to skip this repo for some other reason)")
	}

	// --- Vector 2: cannot warm-hit a row someone ELSE's session caused to
	// exist. --- Seed a 'ready' row for the IDENTICAL fingerprint directly
	// (simulating a legitimate session, elsewhere, having already caused a
	// real build of this exact repo set -- image_builds carries no
	// per-user scoping at all, so this row is indistinguishable, on its
	// own, from one the attacker's own session caused).
	seedReadyImageBuild(ctx, t, imageBuildStore, fingerprint, "narvi/built-image:victim-private-repo")

	session2 := createTestSessionWithRepos(ctx, t, pool, attacker,
		"private-repo", "https://github.com/victim-org/private-repo.git", "main")
	createPendingTurn(ctx, t, turnStore, session2, "read the secrets again")

	provider2 := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-attacker-2"}}
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
		t.Errorf("session2 CreateSpec.Image = %q, want base image %q -- a 'ready' row for this fingerprint exists (seeded, simulating another session's real build), but this attacker session must still be denied its own access check",
			spec2.Image, defaultBaseImage)
	}
	if spec2.SessionConfig.BootMode == sessionconfig.SessionConfigBootModeRepoImage {
		t.Error("session2 SessionConfig.BootMode = repo_image, want anything else -- warm-hitting a ready row the requester cannot themselves read must never happen")
	}
}

// TestRepoAccessGate_LegitimateAccess_WarmBootAndCachedOnRepeat is the
// regression half: a real, enabled, non-viewer creator whose token CAN
// read the repo (CheckRepoAccess allows it) still gets a real warm boot
// off an already-'ready' row, and a SECOND spawn for the same (user,
// repo) does not pay for a second CheckRepoAccess call -- the cache
// (repoaccesscache.go) keeps the steady-state hot path network-free after
// the first check, exactly like §19.1/§19.2's own "zero network calls on
// warm hit" property, just amortized over one check per (user, repo) per
// RepoAccessCacheTTL instead of zero forever.
func TestRepoAccessGate_LegitimateAccess_WarmBootAndCachedOnRepeat(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	creator := createTestUserWithGitHubToken(ctx, t, pool, "gh-fake-token-legit")

	sourceControl := &fakeSourceControl{accessAllowedFor: map[string]bool{"acme/repo1": true}}
	imageBuildStore := narvipg.NewImageBuildStore(pool)
	fingerprint := domainimagebuild.Fingerprint(defaultBaseImage, map[string]string{"repo1": "https://github.com/acme/repo1.git"}, testRuntimeVersion)
	seedReadyImageBuild(ctx, t, imageBuildStore, fingerprint, "narvi/built-image:legit-warm-hit")

	turnStore := narvipg.NewTurnStore(pool)

	// First spawn: a real CheckRepoAccess call is required (nothing cached
	// yet).
	provider1 := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-legit-1"}}
	r1 := newImageBuildTestRegistry(t, ctx, pool, provider1, sourceControl)
	t.Cleanup(func() { _ = r1.Shutdown() })

	session1 := createTestSessionWithRepos(ctx, t, pool, creator,
		"repo1", "https://github.com/acme/repo1.git", "main")
	createPendingTurn(ctx, t, turnStore, session1, "do the thing")

	a1, err := r1.GetOrSpawn(ctx, session1)
	if err != nil {
		t.Fatalf("GetOrSpawn(session1): %v", err)
	}
	sendEnsureDispatched(ctx, t, a1)
	waitUntil(t, 5*time.Second, func() bool { return provider1.callCount() == 1 })

	spec1 := provider1.lastSpec()
	if spec1.Image != "narvi/built-image:legit-warm-hit" {
		t.Fatalf("session1 CreateSpec.Image = %q, want the real ready image %q (creator has real access)", spec1.Image, "narvi/built-image:legit-warm-hit")
	}
	if spec1.SessionConfig.BootMode != sessionconfig.SessionConfigBootModeRepoImage {
		t.Fatalf("session1 SessionConfig.BootMode = %q, want %q", spec1.SessionConfig.BootMode, sessionconfig.SessionConfigBootModeRepoImage)
	}
	firstCallCount := sourceControl.accessCallCount()
	if firstCallCount == 0 {
		t.Fatal("CheckRepoAccess call count = 0 after session1, want at least 1 (first check for this user/repo cannot be cache-hit)")
	}

	// Second spawn: SAME creator, SAME repo, but through the SAME Registry
	// (and therefore the SAME shared *repoAccessCache, registry.go's own
	// "shared across every Actor" design) -- must reuse the cached verdict
	// rather than calling CheckRepoAccess again.
	session2 := createTestSessionWithRepos(ctx, t, pool, creator,
		"repo1", "https://github.com/acme/repo1.git", "main")
	createPendingTurn(ctx, t, turnStore, session2, "do the thing again")

	provider2 := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-legit-2"}}
	// Deliberately reuse r1 (not a fresh Registry) so this session's own
	// Actor shares r1's own repoAccessCache -- a fresh Registry would
	// construct a brand-new, empty cache and defeat the very thing this
	// test proves. Swap the provider only, via a second GetOrSpawn against
	// the SAME Registry for a DIFFERENT session id.
	r1.provider = provider2

	a2, err := r1.GetOrSpawn(ctx, session2)
	if err != nil {
		t.Fatalf("GetOrSpawn(session2): %v", err)
	}
	sendEnsureDispatched(ctx, t, a2)
	waitUntil(t, 5*time.Second, func() bool { return provider2.callCount() == 1 })

	spec2 := provider2.lastSpec()
	if spec2.Image != "narvi/built-image:legit-warm-hit" {
		t.Errorf("session2 CreateSpec.Image = %q, want the real ready image %q", spec2.Image, "narvi/built-image:legit-warm-hit")
	}
	if got := sourceControl.accessCallCount(); got != firstCallCount {
		t.Errorf("CheckRepoAccess call count after session2 = %d, want unchanged at %d (cached verdict must be reused, no added latency on repeat warm-hits)",
			got, firstCallCount)
	}
}

// TestRepoAccessGate_NoCreatedByUser_DeniesWarmBoot proves an automation/
// bot-created session (sessions.created_by IS NULL) is denied warm-boot
// even when a 'ready' row already exists for its exact repo set -- and,
// distinctly from every other denial case, that CheckRepoAccess is never
// even CALLED for this case: there is no creator token to check access
// with in the first place (see imageresolve.go's own repoAccessAllowedForSpawn,
// the createdBy.Valid check that runs before CheckCreatorGuard).
func TestRepoAccessGate_NoCreatedByUser_DeniesWarmBoot(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	sourceControl := &fakeSourceControl{} // defaults to allow -- irrelevant, must never be asked
	imageBuildStore := narvipg.NewImageBuildStore(pool)
	fingerprint := domainimagebuild.Fingerprint(defaultBaseImage, map[string]string{"repo1": "https://github.com/acme/repo1.git"}, testRuntimeVersion)
	seedReadyImageBuild(ctx, t, imageBuildStore, fingerprint, "narvi/built-image:no-creator")

	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-no-creator"}}
	r := newImageBuildTestRegistry(t, ctx, pool, provider, sourceControl)
	t.Cleanup(func() { _ = r.Shutdown() })

	turnStore := narvipg.NewTurnStore(pool)
	sessionID := createTestSessionWithRepos(ctx, t, pool, pgtype.UUID{}, // no creator at all
		"repo1", "https://github.com/acme/repo1.git", "main")
	createPendingTurn(ctx, t, turnStore, sessionID, "automation prompt")

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)
	waitUntil(t, 5*time.Second, func() bool { return provider.callCount() == 1 })

	spec := provider.lastSpec()
	if spec.Image != defaultBaseImage {
		t.Errorf("CreateSpec.Image = %q, want base image %q (no created_by user -> always denied, even against an already-ready row)", spec.Image, defaultBaseImage)
	}
	if spec.SessionConfig.BootMode == sessionconfig.SessionConfigBootModeRepoImage {
		t.Error("SessionConfig.BootMode = repo_image, want anything else")
	}
	if got := sourceControl.accessCallCount(); got != 0 {
		t.Errorf("CheckRepoAccess call count = %d, want 0 (no creator at all means no token to check access with -- the gate must deny before ever reaching SourceControl)", got)
	}
}

// TestRepoAccessGate_DisabledOrViewerCreator_DeniesWarmBootNoAccessCallEither
// proves the two remaining "real creator, but not currently entitled to use
// their own stored GitHub token" cases (CheckCreatorGuard's own §13.3
// viewer-guard staleness recheck) ALSO deny warm-boot outright -- neither
// creates a pending row for a fresh fingerprint, nor warm-hits an
// already-ready one -- and that CheckRepoAccess is never called for
// either, since CheckCreatorGuard denies before decryptCreatorGitHubToken
// (and therefore before any SourceControl call) is ever reached.
//
// This directly supersedes this package's own pre-existing
// TestResolveAndSetImage_CreatorContextIrrelevant_ZeroNetworkCallsRegardless
// (imagebuild_integration_test.go), whose entire premise -- that creator
// context is irrelevant to resolveAndSetImage's outcome -- is exactly the
// vulnerability this batch closes; that test has been replaced (see this
// package's own imagebuild_integration_test.go) rather than left in place
// asserting behavior this fix deliberately makes untrue.
func TestRepoAccessGate_DisabledOrViewerCreator_DeniesWarmBootNoAccessCallEither(t *testing.T) {
	tests := []struct {
		name     string
		setUser  func(t *testing.T, pool *pgxpool.Pool) pgtype.UUID
		repoName string
	}{
		{
			name:     "disabled creator with an otherwise-real, usable github token",
			repoName: "repo-disabled",
			setUser: func(t *testing.T, pool *pgxpool.Pool) pgtype.UUID {
				creator := createTestUserWithGitHubToken(context.Background(), t, pool, "gh-fake-token-disabled-gate")
				if _, err := pool.Exec(context.Background(), `UPDATE users SET disabled = true WHERE id = $1`, creator); err != nil {
					t.Fatalf("disable fixture user: %v", err)
				}
				return creator
			},
		},
		{
			name:     "viewer creator with an otherwise-real, usable github token",
			repoName: "repo-viewer",
			setUser: func(t *testing.T, pool *pgxpool.Pool) pgtype.UUID {
				creator := createTestUserWithGitHubToken(context.Background(), t, pool, "gh-fake-token-viewer-gate")
				if _, err := narvipg.NewUserStore(pool).UpdateRole(context.Background(), creator, sqlcgen.UserRoleViewer); err != nil {
					t.Fatalf("demote fixture user to viewer: %v", err)
				}
				return creator
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pool := newTestPool(t)

			creator := tc.setUser(t, pool)
			repoURL := "https://github.com/acme/" + tc.repoName + ".git"

			sourceControl := &fakeSourceControl{} // defaults to allow -- irrelevant, must never be asked
			provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-" + tc.repoName}}
			r := newImageBuildTestRegistry(t, ctx, pool, provider, sourceControl)
			t.Cleanup(func() { _ = r.Shutdown() })

			turnStore := narvipg.NewTurnStore(pool)
			sessionID := createTestSessionWithRepos(ctx, t, pool, creator, tc.repoName, repoURL, "main")
			createPendingTurn(ctx, t, turnStore, sessionID, "prompt")

			a, err := r.GetOrSpawn(ctx, sessionID)
			if err != nil {
				t.Fatalf("GetOrSpawn: %v", err)
			}
			sendEnsureDispatched(ctx, t, a)
			waitUntil(t, 5*time.Second, func() bool { return provider.callCount() == 1 })

			spec := provider.lastSpec()
			if spec.Image != defaultBaseImage {
				t.Errorf("CreateSpec.Image = %q, want base image %q", spec.Image, defaultBaseImage)
			}

			fingerprint := domainimagebuild.Fingerprint(defaultBaseImage, map[string]string{tc.repoName: repoURL}, testRuntimeVersion)
			imageBuildStore := narvipg.NewImageBuildStore(pool)
			assertNoImageBuildRowAppears(ctx, t, imageBuildStore, fingerprint)

			if got := sourceControl.accessCallCount(); got != 0 {
				t.Errorf("CheckRepoAccess call count = %d, want 0 (CheckCreatorGuard must deny before SourceControl is ever reached)", got)
			}
		})
	}
}

// TestRepoAccessGate_DisabledOrViewerCreator_DeniesWarmHitOfExistingReadyImage
// is test-adversarial audit fix (finding #9)'s own new coverage: the
// pre-existing table test above only ever proves the MINT vector (no
// pending image_builds row is created) for a disabled/viewer creator -- it
// never seeds a pre-existing 'ready' row and so never proves the SECOND
// vector TestRepoAccessGate_NoAccess_CannotMintPendingRowNorWarmHot already
// proves for a genuinely-access-less creator: that a disabled/viewer
// creator is ALSO denied a warm-HIT against an image some OTHER (earlier,
// legitimate) session's build already produced for the identical
// fingerprint. Without this, a plausible future refactor that moved this
// gate to run only inside the "no row yet" branch (never before the
// "ready" warm-hit branch) would leave a disabled/demoted-to-viewer
// creator silently warm-booting into a sandbox containing a repo clone
// from an already-built image -- undetected by the mint-only test alone
// (empirically confirmed during this fix: see this function's own mutation
// test).
func TestRepoAccessGate_DisabledOrViewerCreator_DeniesWarmHitOfExistingReadyImage(t *testing.T) {
	tests := []struct {
		name     string
		setUser  func(t *testing.T, pool *pgxpool.Pool) pgtype.UUID
		repoName string
	}{
		{
			name:     "disabled creator with an otherwise-real, usable github token",
			repoName: "repo-disabled-warmhit",
			setUser: func(t *testing.T, pool *pgxpool.Pool) pgtype.UUID {
				creator := createTestUserWithGitHubToken(context.Background(), t, pool, "gh-fake-token-disabled-warmhit")
				if _, err := pool.Exec(context.Background(), `UPDATE users SET disabled = true WHERE id = $1`, creator); err != nil {
					t.Fatalf("disable fixture user: %v", err)
				}
				return creator
			},
		},
		{
			name:     "viewer creator with an otherwise-real, usable github token",
			repoName: "repo-viewer-warmhit",
			setUser: func(t *testing.T, pool *pgxpool.Pool) pgtype.UUID {
				creator := createTestUserWithGitHubToken(context.Background(), t, pool, "gh-fake-token-viewer-warmhit")
				if _, err := narvipg.NewUserStore(pool).UpdateRole(context.Background(), creator, sqlcgen.UserRoleViewer); err != nil {
					t.Fatalf("demote fixture user to viewer: %v", err)
				}
				return creator
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pool := newTestPool(t)

			creator := tc.setUser(t, pool)
			repoURL := "https://github.com/acme/" + tc.repoName + ".git"

			// Seed an already-'ready' row for this exact fingerprint FIRST
			// -- simulating some OTHER, earlier, legitimate session having
			// already caused a real build of this repo set, exactly like
			// TestRepoAccessGate_NoAccess_CannotMintPendingRowNorWarmHit's
			// own "Vector 2".
			fingerprint := domainimagebuild.Fingerprint(defaultBaseImage, map[string]string{tc.repoName: repoURL}, testRuntimeVersion)
			imageBuildStore := narvipg.NewImageBuildStore(pool)
			if err := imageBuildStore.UpsertPending(ctx, sqlcgen.UpsertPendingImageBuildParams{
				Fingerprint: fingerprint, Base: defaultBaseImage,
				RepoUrls:       mustMarshalRepoURLs(t, map[string]string{tc.repoName: "https://github.com/acme/" + tc.repoName}),
				RuntimeVersion: testRuntimeVersion,
			}); err != nil {
				t.Fatalf("seed pending image_builds row: %v", err)
			}
			if _, err := imageBuildStore.Claim(ctx, fingerprint); err != nil {
				t.Fatalf("claim image_builds row: %v", err)
			}
			builtImageRef := "narvi/built-image:" + tc.repoName
			builtRepoSHAs := mustMarshalRepoURLs(t, map[string]string{tc.repoName: "sha-warmhit"})
			if _, err := imageBuildStore.RecordSuccess(ctx, sqlcgen.RecordImageBuildSuccessParams{
				Fingerprint: fingerprint, ImageRef: &builtImageRef, BuiltRepoShas: builtRepoSHAs,
				BuiltAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
			}); err != nil {
				t.Fatalf("record image_builds success: %v", err)
			}

			sourceControl := &fakeSourceControl{} // defaults to allow -- irrelevant, must never be asked
			provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-" + tc.repoName}}
			r := newImageBuildTestRegistry(t, ctx, pool, provider, sourceControl)
			t.Cleanup(func() { _ = r.Shutdown() })

			turnStore := narvipg.NewTurnStore(pool)
			sessionID := createTestSessionWithRepos(ctx, t, pool, creator, tc.repoName, repoURL, "main")
			createPendingTurn(ctx, t, turnStore, sessionID, "prompt")

			a, err := r.GetOrSpawn(ctx, sessionID)
			if err != nil {
				t.Fatalf("GetOrSpawn: %v", err)
			}
			sendEnsureDispatched(ctx, t, a)
			waitUntil(t, 5*time.Second, func() bool { return provider.callCount() == 1 })

			spec := provider.lastSpec()
			if spec.Image != defaultBaseImage {
				t.Errorf("CreateSpec.Image = %q, want base image %q -- a real 'ready' row exists for this fingerprint, but a disabled/viewer creator must still be denied warm-boot",
					spec.Image, defaultBaseImage)
			}
			if spec.SessionConfig.BootMode == sessionconfig.SessionConfigBootModeRepoImage {
				t.Error("SessionConfig.BootMode = repo_image, want anything else -- warm-hitting a ready row must never happen for a disabled/viewer creator")
			}

			if got := sourceControl.accessCallCount(); got != 0 {
				t.Errorf("CheckRepoAccess call count = %d, want 0 (CheckCreatorGuard must deny before SourceControl is ever reached)", got)
			}
		})
	}
}

// mustMarshalRepoURLs is a small json.Marshal-or-Fatal helper shared by
// this file's own image_builds row seeding (mirrors imagebuild_
// integration_test.go's own seedReadyImageBuild inline marshal calls).
func mustMarshalRepoURLs(t *testing.T, m map[string]string) []byte {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal repo urls: %v", err)
	}
	return raw
}

// testRepoSpec is one repo entry for createTestSessionWithMultipleRepos
// below (name/url/branch, mirroring reposJSONForTest's own single-repo
// shape).
type testRepoSpec struct {
	Name, URL, Branch string
}

// createTestSessionWithMultipleRepos is reposJSONForTest/
// createTestSessionWithRepos's own multi-repo generalization -- neither
// existing helper supports more than one repo, which this file's own
// multi-repo regression tests (test-adversarial audit fix, finding #3)
// need.
func createTestSessionWithMultipleRepos(ctx context.Context, t *testing.T, pool *pgxpool.Pool, createdBy pgtype.UUID, repos []testRepoSpec) pgtype.UUID {
	t.Helper()

	type repoJSON struct {
		Name   string `json:"name"`
		URL    string `json:"url"`
		Branch string `json:"branch"`
	}
	list := make([]repoJSON, 0, len(repos))
	for _, r := range repos {
		list = append(list, repoJSON{Name: r.Name, URL: r.URL, Branch: r.Branch})
	}
	raw, err := json.Marshal(list)
	if err != nil {
		t.Fatalf("marshal multi-repo test repos: %v", err)
	}

	created, err := narvipg.NewSessionStore(pool).Create(ctx, sqlcgen.CreateSessionParams{
		SpawnSource: sqlcgen.SessionSpawnSourceWeb,
		CreatedBy:   createdBy,
		Repos:       raw,
	})
	if err != nil {
		t.Fatalf("create multi-repo test session: %v", err)
	}
	return created.ID
}

// TestRepoAccessGate_MultiRepo_AllAllowed_ChecksEveryRepoNotJustOne is
// test-adversarial audit fix (finding #3)'s own deterministic (map-
// iteration-order-independent) regression test for the "requires ALL
// repos to pass, not just one" guarantee: with every one of N repos
// genuinely allowed (no denial anywhere to short-circuit on), the gate
// must still call CheckRepoAccess for EVERY single one of them, not stop
// early after the first ALLOW -- a future refactor that (accidentally)
// short-circuits on the first allowed repo would make accessCallCount
// come out LESS than the repo count here, REGARDLESS of which repo Go's
// own randomized map iteration happens to visit first (unlike a mixed
// allow/deny scenario, whose observable outcome legitimately DOES depend
// on visitation order for the CORRECT implementation too -- see this
// file's own TestRepoAccessGate_MultiRepo_OneDenied_DeniesRegardlessOfOrder
// for that complementary case).
func TestRepoAccessGate_MultiRepo_AllAllowed_ChecksEveryRepoNotJustOne(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	creator := createTestUserWithGitHubToken(ctx, t, pool, "gh-fake-token-multi-allow")

	const repoCount = 4
	repoURLs := make(map[string]string, repoCount)
	repos := make([]testRepoSpec, 0, repoCount)
	accessAllowedFor := make(map[string]bool, repoCount)
	for i := 0; i < repoCount; i++ {
		name := fmt.Sprintf("repo%d", i)
		url := fmt.Sprintf("https://github.com/acme/%s.git", name)
		repoURLs[name] = url
		repos = append(repos, testRepoSpec{Name: name, URL: url, Branch: "main"})
		accessAllowedFor["acme/"+name] = true
	}

	sourceControl := &fakeSourceControl{accessAllowedFor: accessAllowedFor}
	fingerprint := domainimagebuild.Fingerprint(defaultBaseImage, repoURLs, testRuntimeVersion)
	imageBuildStore := narvipg.NewImageBuildStore(pool)
	seedReadyImageBuild(ctx, t, imageBuildStore, fingerprint, "narvi/built-image:multi-allow")

	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-multi-allow"}}
	r := newImageBuildTestRegistry(t, ctx, pool, provider, sourceControl)
	t.Cleanup(func() { _ = r.Shutdown() })

	turnStore := narvipg.NewTurnStore(pool)
	sessionID := createTestSessionWithMultipleRepos(ctx, t, pool, creator, repos)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)
	waitUntil(t, 5*time.Second, func() bool { return provider.callCount() == 1 })

	if got := sourceControl.accessCallCount(); got != repoCount {
		t.Errorf("CheckRepoAccess call count = %d, want exactly %d (every repo must be checked, not just one before short-circuiting to allow)",
			got, repoCount)
	}
}

// TestRepoAccessGate_MultiRepo_OneDenied_DeniesRegardlessOfOrder is
// test-adversarial audit fix (finding #3)'s companion multi-repo test: a
// session naming several repos, exactly one of which is genuinely denied,
// must have its warm-boot denied overall -- true regardless of which repo
// Go's own randomized map iteration visits first (an allowed repo visited
// first just `continue`s in the correct implementation; the denied repo,
// whenever reached, still ends the whole check in a deny).
func TestRepoAccessGate_MultiRepo_OneDenied_DeniesRegardlessOfOrder(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	creator := createTestUserWithGitHubToken(ctx, t, pool, "gh-fake-token-multi-deny")

	sourceControl := &fakeSourceControl{accessAllowedFor: map[string]bool{
		"acme/allowed-repo": true,
		"acme/denied-repo":  false,
	}}
	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-multi-deny"}}
	r := newImageBuildTestRegistry(t, ctx, pool, provider, sourceControl)
	t.Cleanup(func() { _ = r.Shutdown() })

	turnStore := narvipg.NewTurnStore(pool)
	sessionID := createTestSessionWithMultipleRepos(ctx, t, pool, creator, []testRepoSpec{
		{Name: "allowed-repo", URL: "https://github.com/acme/allowed-repo.git", Branch: "main"},
		{Name: "denied-repo", URL: "https://github.com/acme/denied-repo.git", Branch: "main"},
	})
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)
	waitUntil(t, 5*time.Second, func() bool { return provider.callCount() == 1 })

	spec := provider.lastSpec()
	if spec.Image != defaultBaseImage {
		t.Errorf("CreateSpec.Image = %q, want base image %q -- ALL repos must be allowed, one denial must deny the whole spawn",
			spec.Image, defaultBaseImage)
	}
	if spec.SessionConfig.BootMode == sessionconfig.SessionConfigBootModeRepoImage {
		t.Error("SessionConfig.BootMode = repo_image, want anything else")
	}
	if got := sourceControl.accessCallCount(); got == 0 {
		t.Fatal("CheckRepoAccess call count = 0, want at least 1")
	}
}

// TestRepoAccessGate_IndeterminateSCMError_DeniesThisSpawnButNeverCaches is
// test-adversarial audit fix (finding #3/#10)'s own regression test for
// imageresolve.go's most safety-critical, and previously entirely
// untested, branch: a CheckRepoAccess call that itself fails (network/
// timeout/5xx) must deny only THIS spawn -- never a cached, sticky deny --
// so the VERY NEXT spawn attempt re-checks live. fakeSourceControl's own
// accessErr field (pushpr_integration_test.go) was built for exactly this
// purpose but no test ever set it before this fix.
func TestRepoAccessGate_IndeterminateSCMError_DeniesThisSpawnButNeverCaches(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	creator := createTestUserWithGitHubToken(ctx, t, pool, "gh-fake-token-indeterminate")
	repoURL := "https://github.com/acme/flaky-repo.git"

	sourceControl := &fakeSourceControl{accessErr: errors.New("simulated network timeout")}
	fingerprint := domainimagebuild.Fingerprint(defaultBaseImage, map[string]string{"repo1": repoURL}, testRuntimeVersion)
	imageBuildStore := narvipg.NewImageBuildStore(pool)
	seedReadyImageBuild(ctx, t, imageBuildStore, fingerprint, "narvi/built-image:indeterminate")

	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-indeterminate-1"}}
	r := newImageBuildTestRegistry(t, ctx, pool, provider, sourceControl)
	t.Cleanup(func() { _ = r.Shutdown() })

	turnStore := narvipg.NewTurnStore(pool)

	// --- First spawn: CheckRepoAccess fails indeterminately -> denied,
	// but must NOT be cached. ---
	session1 := createTestSessionWithRepos(ctx, t, pool, creator, "repo1", repoURL, "main")
	createPendingTurn(ctx, t, turnStore, session1, "prompt 1")

	a1, err := r.GetOrSpawn(ctx, session1)
	if err != nil {
		t.Fatalf("GetOrSpawn(session1): %v", err)
	}
	sendEnsureDispatched(ctx, t, a1)
	waitUntil(t, 5*time.Second, func() bool { return provider.callCount() == 1 })

	spec1 := provider.lastSpec()
	if spec1.Image != defaultBaseImage {
		t.Errorf("session1 CreateSpec.Image = %q, want base image %q (an indeterminate SCM failure must deny THIS spawn)", spec1.Image, defaultBaseImage)
	}
	firstCallCount := sourceControl.accessCallCount()
	if firstCallCount == 0 {
		t.Fatal("CheckRepoAccess call count = 0, want at least 1")
	}

	// --- Second spawn, SAME (user, repo), but the SCM is now healthy and
	// allows access: if the first failure had been wrongly cached as a
	// deny, this would still be denied with NO further CheckRepoAccess
	// call. A LIVE re-check (call count increases, spec now uses the
	// real ready image) proves the failure was never cached. ---
	sourceControl.mu.Lock()
	sourceControl.accessErr = nil
	sourceControl.accessAllowedFor = map[string]bool{"acme/flaky-repo": true}
	sourceControl.mu.Unlock()

	session2 := createTestSessionWithRepos(ctx, t, pool, creator, "repo1", repoURL, "main")
	createPendingTurn(ctx, t, turnStore, session2, "prompt 2")

	provider2 := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-indeterminate-2"}}
	r.provider = provider2

	a2, err := r.GetOrSpawn(ctx, session2)
	if err != nil {
		t.Fatalf("GetOrSpawn(session2): %v", err)
	}
	sendEnsureDispatched(ctx, t, a2)
	waitUntil(t, 5*time.Second, func() bool { return provider2.callCount() == 1 })

	spec2 := provider2.lastSpec()
	if spec2.Image != "narvi/built-image:indeterminate" {
		t.Errorf("session2 CreateSpec.Image = %q, want the real ready image %q -- the prior indeterminate failure must NEVER have been cached as a deny",
			spec2.Image, "narvi/built-image:indeterminate")
	}
	if got := sourceControl.accessCallCount(); got <= firstCallCount {
		t.Errorf("CheckRepoAccess call count after session2 = %d, want > %d (a live re-check must happen; a cached deny would have skipped it)",
			got, firstCallCount)
	}
}

// TestRepoAccessGate_RepeatedIndeterminateFailures_CircuitBreakerSkipsFurtherNetworkCalls
// is correctness-availability audit fix (finding #5)'s own regression
// test: once CheckRepoAccess has failed indeterminately
// repoAccessCheckBreakerThreshold times in a row, within
// RepoAccessCheckBreakerWindow, the breaker OPENS and every further
// spawn attempt during that window must deny WITHOUT calling
// CheckRepoAccess again -- damping the "up to len(repos) * timeout of
// sequential GitHub latency per spawn" cost a sustained outage would
// otherwise reintroduce on EVERY subsequent spawn.
func TestRepoAccessGate_RepeatedIndeterminateFailures_CircuitBreakerSkipsFurtherNetworkCalls(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	creator := createTestUserWithGitHubToken(ctx, t, pool, "gh-fake-token-breaker")
	repoURL := "https://github.com/acme/outage-repo.git"

	sourceControl := &fakeSourceControl{accessErr: errors.New("simulated sustained outage")}
	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-breaker"}}
	r := newImageBuildTestRegistry(t, ctx, pool, provider, sourceControl)
	t.Cleanup(func() { _ = r.Shutdown() })

	turnStore := narvipg.NewTurnStore(pool)

	// Drive repoAccessCheckBreakerThreshold-plus-a-few spawns, each for a
	// DIFFERENT session (same creator/repo, so each is its own genuine
	// cache miss -- see TestRepoAccessGate_IndeterminateSCMError above for
	// why an indeterminate failure is never cached on its own). Once the
	// breaker trips, accessCallCount must stop increasing even though more
	// spawns keep happening.
	const attempts = repoAccessCheckBreakerThreshold + 3
	var lastCallCount int
	for i := 0; i < attempts; i++ {
		sessionID := createTestSessionWithRepos(ctx, t, pool, creator, "repo1", repoURL, "main")
		createPendingTurn(ctx, t, turnStore, sessionID, fmt.Sprintf("prompt %d", i))

		attemptProvider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: fmt.Sprintf("provider-breaker-%d", i)}}
		r.provider = attemptProvider

		a, err := r.GetOrSpawn(ctx, sessionID)
		if err != nil {
			t.Fatalf("GetOrSpawn(attempt %d): %v", i, err)
		}
		sendEnsureDispatched(ctx, t, a)
		waitUntil(t, 5*time.Second, func() bool { return attemptProvider.callCount() == 1 })

		if got := attemptProvider.lastSpec().Image; got != defaultBaseImage {
			t.Fatalf("attempt %d: CreateSpec.Image = %q, want base image %q", i, got, defaultBaseImage)
		}
		lastCallCount = sourceControl.accessCallCount()
	}

	if lastCallCount >= attempts {
		t.Errorf("CheckRepoAccess call count after %d attempts = %d, want fewer than %d (the circuit breaker must have opened and skipped at least one live call)",
			attempts, lastCallCount, attempts)
	}
	if lastCallCount < repoAccessCheckBreakerThreshold {
		t.Errorf("CheckRepoAccess call count = %d, want at least %d (the breaker must not trip BEFORE the threshold is reached)",
			lastCallCount, repoAccessCheckBreakerThreshold)
	}
}

// TestRepoAccessAllowedForSpawn_BoundsCheckRepoAccessWithRepoAccessCheckTimeout
// is test-adversarial audit fix (finding #11)'s own regression test:
// CheckRepoAccess must be called with a ctx bounded by platform.Timeouts.
// RepoAccessCheckTimeout (checkCtx in imageresolve.go), never the actor's
// own unbounded outer ctx -- fakeSourceControl.lastAccessCtxDeadline
// (pushpr_integration_test.go) captures exactly the ctx this gate actually
// hands CheckRepoAccess, so this test can verify a real deadline exists
// and is no looser than RepoAccessCheckTimeout, which context.Background()
// (this test's own outer ctx, no deadline at all) could never satisfy by
// accident.
func TestRepoAccessAllowedForSpawn_BoundsCheckRepoAccessWithRepoAccessCheckTimeout(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	creator := createTestUserWithGitHubToken(ctx, t, pool, "gh-fake-token-timeout")
	repoURL := "https://github.com/acme/timeout-repo.git"

	sourceControl := &fakeSourceControl{accessAllowedFor: map[string]bool{"acme/timeout-repo": true}}
	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-timeout"}}
	r := newImageBuildTestRegistry(t, ctx, pool, provider, sourceControl)
	t.Cleanup(func() { _ = r.Shutdown() })

	turnStore := narvipg.NewTurnStore(pool)
	sessionID := createTestSessionWithRepos(ctx, t, pool, creator, "repo1", repoURL, "main")
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)
	waitUntil(t, 5*time.Second, func() bool { return provider.callCount() == 1 })

	deadline, ok := sourceControl.lastAccessCtxDeadline()
	if !ok {
		t.Fatal("CheckRepoAccess ctx had no deadline at all -- want one bounded by RepoAccessCheckTimeout, got the actor's own unbounded outer ctx")
	}

	timeouts := platform.DefaultTimeouts()
	remaining := time.Until(deadline)
	// Generous slack for real wall-clock time elapsed between the deadline
	// being set and this assertion running -- this only needs to prove
	// the deadline is NOT the unbounded outer ctx (which would report
	// ok=false above) and is roughly RepoAccessCheckTimeout-scaled, not
	// pin the exact remaining microseconds.
	if remaining <= 0 || remaining > timeouts.RepoAccessCheckTimeout {
		t.Errorf("CheckRepoAccess ctx remaining deadline = %v, want in (0, %v] (RepoAccessCheckTimeout)", remaining, timeouts.RepoAccessCheckTimeout)
	}
}

// TestRepoAccessGate_CreatorWithNoGitHubIdentity_DeniesWarmBootNoAccessCall
// is test-adversarial audit fix (finding #12)'s own regression test: a
// real, enabled, non-viewer creator who simply has no linked GitHub
// identity at all (decryptCreatorGitHubToken's identity.
// GetByUserAndProvider lookup fails) must be denied warm-boot exactly like
// every other "nothing usable to check access with" case -- every OTHER
// gate test in this file builds its creator via
// createTestUserWithGitHubToken, which always has a real, usable token;
// this is the one sub-case (real creator, no token at all) none of them
// exercise.
func TestRepoAccessGate_CreatorWithNoGitHubIdentity_DeniesWarmBootNoAccessCall(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	// A real, enabled, non-viewer user -- but with NO linked GitHub
	// identity at all (unlike createTestUserWithGitHubToken's own fixture).
	creator, err := narvipg.NewUserStore(pool).Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: fmt.Sprintf("no-identity-gate-test-%d@example.com", time.Now().UnixNano()),
		DisplayName:  "No Identity Test User",
		Role:         sqlcgen.UserRoleMember,
	})
	if err != nil {
		t.Fatalf("create user with no identity: %v", err)
	}

	sourceControl := &fakeSourceControl{} // defaults to allow -- irrelevant, must never be asked
	fingerprint := domainimagebuild.Fingerprint(defaultBaseImage, map[string]string{"repo1": "https://github.com/acme/repo1.git"}, testRuntimeVersion)
	imageBuildStore := narvipg.NewImageBuildStore(pool)
	seedReadyImageBuild(ctx, t, imageBuildStore, fingerprint, "narvi/built-image:no-identity")

	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-no-identity"}}
	r := newImageBuildTestRegistry(t, ctx, pool, provider, sourceControl)
	t.Cleanup(func() { _ = r.Shutdown() })

	turnStore := narvipg.NewTurnStore(pool)
	sessionID := createTestSessionWithRepos(ctx, t, pool, creator.ID, "repo1", "https://github.com/acme/repo1.git", "main")
	createPendingTurn(ctx, t, turnStore, sessionID, "prompt")

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)
	waitUntil(t, 5*time.Second, func() bool { return provider.callCount() == 1 })

	spec := provider.lastSpec()
	if spec.Image != defaultBaseImage {
		t.Errorf("CreateSpec.Image = %q, want base image %q (no linked GitHub identity -> no usable token -> deny)", spec.Image, defaultBaseImage)
	}
	if got := sourceControl.accessCallCount(); got != 0 {
		t.Errorf("CheckRepoAccess call count = %d, want 0 (no usable token means the gate must deny before ever reaching SourceControl)", got)
	}
}

// TestRepoAccessGate_AllCacheHit_SkipsIdentityLookupAndDecrypt is
// correctness-availability audit fix (finding #7)'s own regression test:
// once every repo in a spawn already has a cached verdict, the gate must
// not decrypt (or even look up) the creator's GitHub identity/token at all
// -- proved here by DELETING the creator's own identity row AFTER the
// first spawn already cached an ALLOW verdict, then spawning again for the
// SAME (user, repo): if the fix regressed to eagerly decrypting the token
// on every call, this second spawn would suddenly deny (the identity row
// is gone), which is exactly what this test would catch.
func TestRepoAccessGate_AllCacheHit_SkipsIdentityLookupAndDecrypt(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	creator := createTestUserWithGitHubToken(ctx, t, pool, "gh-fake-token-lazy-decrypt")
	repoURL := "https://github.com/acme/lazy-decrypt-repo.git"

	sourceControl := &fakeSourceControl{accessAllowedFor: map[string]bool{"acme/lazy-decrypt-repo": true}}
	fingerprint := domainimagebuild.Fingerprint(defaultBaseImage, map[string]string{"repo1": repoURL}, testRuntimeVersion)
	imageBuildStore := narvipg.NewImageBuildStore(pool)
	seedReadyImageBuild(ctx, t, imageBuildStore, fingerprint, "narvi/built-image:lazy-decrypt")

	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-lazy-decrypt-1"}}
	r := newImageBuildTestRegistry(t, ctx, pool, provider, sourceControl)
	t.Cleanup(func() { _ = r.Shutdown() })

	turnStore := narvipg.NewTurnStore(pool)

	// First spawn: real, live CheckRepoAccess call, caches ALLOW.
	session1 := createTestSessionWithRepos(ctx, t, pool, creator, "repo1", repoURL, "main")
	createPendingTurn(ctx, t, turnStore, session1, "prompt 1")

	a1, err := r.GetOrSpawn(ctx, session1)
	if err != nil {
		t.Fatalf("GetOrSpawn(session1): %v", err)
	}
	sendEnsureDispatched(ctx, t, a1)
	waitUntil(t, 5*time.Second, func() bool { return provider.callCount() == 1 })

	if got := provider.lastSpec().Image; got != "narvi/built-image:lazy-decrypt" {
		t.Fatalf("session1 CreateSpec.Image = %q, want the real ready image", got)
	}
	firstCallCount := sourceControl.accessCallCount()
	if firstCallCount == 0 {
		t.Fatal("CheckRepoAccess call count = 0, want at least 1 for the first, uncached check")
	}

	// Delete the creator's own GitHub identity row entirely -- if the
	// fix regressed to eagerly decrypting/looking up the token on EVERY
	// call (not lazily, only on an actual cache miss), the NEXT spawn
	// below would now fail to find a usable token and deny.
	if _, err := pool.Exec(ctx, `DELETE FROM identities WHERE user_id = $1`, creator); err != nil {
		t.Fatalf("delete creator identity row: %v", err)
	}

	session2 := createTestSessionWithRepos(ctx, t, pool, creator, "repo1", repoURL, "main")
	createPendingTurn(ctx, t, turnStore, session2, "prompt 2")

	provider2 := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-lazy-decrypt-2"}}
	r.provider = provider2

	a2, err := r.GetOrSpawn(ctx, session2)
	if err != nil {
		t.Fatalf("GetOrSpawn(session2): %v", err)
	}
	sendEnsureDispatched(ctx, t, a2)
	waitUntil(t, 5*time.Second, func() bool { return provider2.callCount() == 1 })

	spec2 := provider2.lastSpec()
	if spec2.Image != "narvi/built-image:lazy-decrypt" {
		t.Errorf("session2 CreateSpec.Image = %q, want the real ready image %q -- the cached verdict must be used WITHOUT ever needing the (now-deleted) identity row",
			spec2.Image, "narvi/built-image:lazy-decrypt")
	}
	if got := sourceControl.accessCallCount(); got != firstCallCount {
		t.Errorf("CheckRepoAccess call count after session2 = %d, want unchanged at %d (an all-cache-hit spawn must make no further live calls)",
			got, firstCallCount)
	}
}

// TestRepoAccessGate_CacheHit_StillDeniesWhenCreatorSubsequentlyDisabled is
// the essential companion/guard-rail for the lazy-decrypt optimization
// above (correctness-availability audit fix, finding #7): CheckCreatorGuard
// itself must NEVER be skipped on a cache hit, even though the token
// decrypt now is -- repoAccessCache only ever remembers a REPO-read
// verdict, never the creator's own current enabled/role status, so a
// creator disabled AFTER their repo access was cached must still be
// caught on the very next spawn. Without this test, an over-eager future
// application of the SAME "skip on cache hit" optimization to
// CheckCreatorGuard as well (not just the token decrypt) would silently
// reopen the exact §13.3 staleness hole this whole gate exists to close.
func TestRepoAccessGate_CacheHit_StillDeniesWhenCreatorSubsequentlyDisabled(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	creator := createTestUserWithGitHubToken(ctx, t, pool, "gh-fake-token-cachehit-disabled")
	repoURL := "https://github.com/acme/cachehit-disabled-repo.git"

	sourceControl := &fakeSourceControl{accessAllowedFor: map[string]bool{"acme/cachehit-disabled-repo": true}}
	fingerprint := domainimagebuild.Fingerprint(defaultBaseImage, map[string]string{"repo1": repoURL}, testRuntimeVersion)
	imageBuildStore := narvipg.NewImageBuildStore(pool)
	seedReadyImageBuild(ctx, t, imageBuildStore, fingerprint, "narvi/built-image:cachehit-disabled")

	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-cachehit-disabled-1"}}
	r := newImageBuildTestRegistry(t, ctx, pool, provider, sourceControl)
	t.Cleanup(func() { _ = r.Shutdown() })

	turnStore := narvipg.NewTurnStore(pool)

	// First spawn: caches ALLOW while the creator is still enabled.
	session1 := createTestSessionWithRepos(ctx, t, pool, creator, "repo1", repoURL, "main")
	createPendingTurn(ctx, t, turnStore, session1, "prompt 1")

	a1, err := r.GetOrSpawn(ctx, session1)
	if err != nil {
		t.Fatalf("GetOrSpawn(session1): %v", err)
	}
	sendEnsureDispatched(ctx, t, a1)
	waitUntil(t, 5*time.Second, func() bool { return provider.callCount() == 1 })

	if got := provider.lastSpec().Image; got != "narvi/built-image:cachehit-disabled" {
		t.Fatalf("session1 CreateSpec.Image = %q, want the real ready image", got)
	}

	// Disable the creator AFTER caching -- the cache still holds ALLOW for
	// this (user, repo), but CheckCreatorGuard must catch this fresh.
	if _, err := pool.Exec(ctx, `UPDATE users SET disabled = true WHERE id = $1`, creator); err != nil {
		t.Fatalf("disable creator: %v", err)
	}

	session2 := createTestSessionWithRepos(ctx, t, pool, creator, "repo1", repoURL, "main")
	createPendingTurn(ctx, t, turnStore, session2, "prompt 2")

	provider2 := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-cachehit-disabled-2"}}
	r.provider = provider2

	a2, err := r.GetOrSpawn(ctx, session2)
	if err != nil {
		t.Fatalf("GetOrSpawn(session2): %v", err)
	}
	sendEnsureDispatched(ctx, t, a2)
	waitUntil(t, 5*time.Second, func() bool { return provider2.callCount() == 1 })

	spec2 := provider2.lastSpec()
	if spec2.Image != defaultBaseImage {
		t.Errorf("session2 CreateSpec.Image = %q, want base image %q -- a disabled creator must be denied EVEN THOUGH repo access was already cached as allowed",
			spec2.Image, defaultBaseImage)
	}
}

// TestRepoAccessGate_UnsupportedRepoHost_DeniesWarmBootNoAccessCall is
// security-adversarial audit fix (finding #2)'s own regression test: a
// session whose repo URL names a host OTHER than the one this gate's
// configured SourceControl implementation actually queries (githubapi's
// real api.github.com) must be denied outright, and CheckRepoAccess must
// never even be called -- proving this gate's fail-closed behavior for a
// non-GitHub repo URL is a DESIGNED property (repoURLHostAllowed), not an
// accident of today's single-adapter roster.
func TestRepoAccessGate_UnsupportedRepoHost_DeniesWarmBootNoAccessCall(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	creator := createTestUserWithGitHubToken(ctx, t, pool, "gh-fake-token-unsupported-host")
	repoURL := "https://example.org/acme/tools.git" // passes reposource.ValidateRepoURL (any https host), but is NOT github.com

	sourceControl := &fakeSourceControl{} // defaults to allow -- irrelevant, must never be asked
	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-unsupported-host"}}
	r := newImageBuildTestRegistry(t, ctx, pool, provider, sourceControl)
	t.Cleanup(func() { _ = r.Shutdown() })

	turnStore := narvipg.NewTurnStore(pool)
	sessionID := createTestSessionWithRepos(ctx, t, pool, creator, "tools", repoURL, "main")
	createPendingTurn(ctx, t, turnStore, sessionID, "prompt")

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)
	waitUntil(t, 5*time.Second, func() bool { return provider.callCount() == 1 })

	spec := provider.lastSpec()
	if spec.Image != defaultBaseImage {
		t.Errorf("CreateSpec.Image = %q, want base image %q (unsupported repo-url host must deny outright)", spec.Image, defaultBaseImage)
	}

	fingerprint := domainimagebuild.Fingerprint(defaultBaseImage, map[string]string{"tools": repoURL}, testRuntimeVersion)
	imageBuildStore := narvipg.NewImageBuildStore(pool)
	assertNoImageBuildRowAppears(ctx, t, imageBuildStore, fingerprint)

	if got := sourceControl.accessCallCount(); got != 0 {
		t.Errorf("CheckRepoAccess call count = %d, want 0 (an unsupported host must deny before ever deriving owner/repo or calling SourceControl)", got)
	}
}

// TestRepoAccessGate_UnsupportedRepoHost_GitlabExampleCom_DeniesWarmBoot is
// audit-remediation batch B3 round 2's own regression test for finding #7
// (allowlist drift between this gate and imagebuild.Builder.
// resolveRepoSHAs' own identical gate): deliberately uses
// "gitlab.example.com" -- the EXACT unsupported-host fixture
// internal/app/imagebuild/builder_integration_test.go's own
// TestPumpOnce_RepoBearingRow_UnsupportedHost_FailsCleanlyNeverCallsSourceControl
// uses -- rather than this file's own pre-existing "example.org" fixture
// (TestRepoAccessGate_UnsupportedRepoHost_DeniesWarmBootNoAccessCall,
// above). Before this batch, an adversarial widening of ONLY this gate's
// own reposource.CheckRepoHost call (to also accept "gitlab.example.com")
// would have passed the ENTIRE existing suite, since no test here ever
// exercised that specific host -- this test exists specifically to close
// that gap, and now that both gates route through the shared
// ports.SupportedSourceControlHosts() (see that function's own doc
// comment), the two can no longer drift apart structurally either.
func TestRepoAccessGate_UnsupportedRepoHost_GitlabExampleCom_DeniesWarmBoot(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	creator := createTestUserWithGitHubToken(ctx, t, pool, "gh-fake-token-gitlab-host")
	repoURL := "https://gitlab.example.com/acme/widgets.git" // same fixture imagebuild's own equivalent gate test uses

	sourceControl := &fakeSourceControl{} // defaults to allow -- irrelevant, must never be asked
	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-gitlab-host"}}
	r := newImageBuildTestRegistry(t, ctx, pool, provider, sourceControl)
	t.Cleanup(func() { _ = r.Shutdown() })

	turnStore := narvipg.NewTurnStore(pool)
	sessionID := createTestSessionWithRepos(ctx, t, pool, creator, "widgets", repoURL, "main")
	createPendingTurn(ctx, t, turnStore, sessionID, "prompt")

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)
	waitUntil(t, 5*time.Second, func() bool { return provider.callCount() == 1 })

	spec := provider.lastSpec()
	if spec.Image != defaultBaseImage {
		t.Errorf("CreateSpec.Image = %q, want base image %q (unsupported repo-url host must deny outright)", spec.Image, defaultBaseImage)
	}

	fingerprint := domainimagebuild.Fingerprint(defaultBaseImage, map[string]string{"widgets": repoURL}, testRuntimeVersion)
	imageBuildStore := narvipg.NewImageBuildStore(pool)
	assertNoImageBuildRowAppears(ctx, t, imageBuildStore, fingerprint)

	if got := sourceControl.accessCallCount(); got != 0 {
		t.Errorf("CheckRepoAccess call count = %d, want 0 (an unsupported host must deny before ever deriving owner/repo or calling SourceControl)", got)
	}
}
