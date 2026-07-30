// This file (imageresolve.go) implements Step 26's ("image builds",
// §8.5-note/§10-P2) own real per-spawn image selection: dispatch.go's
// planFreshSpawn/planRestore both build their CreateSpec with
// Image=defaultBaseImage (unconditionally, exactly as before this Step),
// and handleEnsureDispatched then calls resolveAndSetImage (below) to
// CORRECT that field in place -- to a real, previously-built image_ref, if
// (and only if) one is already known-ready for this exact spawn's own
// fingerprint -- immediately before executeSpawn/executeRestore ever calls
// the real provider.
//
// # Step 41 ("warm boot: shared fingerprint + spawn-path simplification",
// §19.1) rewrite -- what changed and why
//
// Before Step 41, computing a fingerprint here required a per-repo
// `ResolveBranchSHA` GitHub API call (up to
// len(repos)*platform.Timeouts.RepoSHAResolutionTimeout of sequential
// network latency per spawn), which in turn required a usable creator
// GitHub token and a fresh CheckCreatorGuard recheck before ever touching
// it. §19.1 redefines domain/imagebuild.Fingerprint to hash each repo's
// normalized clone URL instead of a resolved SHA (one shared image per
// repo SET, not per exact SHA combination) -- so the fingerprint is now
// computable directly from plan.spec.SessionConfig.Repos alone, with ZERO
// network calls, for EVERY spawn, not just a lucky warm-hit one. This is
// §19.2's own explicit promise: "removes up to len(repos) *
// RepoSHAResolutionTimeout ... of sequential GitHub latency from every
// spawn attempt ... removes the 'creator has no GitHub token -> cold
// boot' fallback class entirely."
//
// This function therefore no longer calls ResolveBranchSHA, CheckCreatorGuard,
// or decryptCreatorGitHubToken AT ALL -- there is no creator/token
// dependency left in this file (confirmed via grep before removal: no
// other function in this file used them for any other reason). Those
// three remain exactly as they were for pushpr.go/contractdrift.go/
// scmcredentials.go's own, unrelated call sites (githubtoken.go).
//
// # Step 41/42 boundary (§19.1 vs §19.9) -- documented design decision
//
// §19.1's own prose describes the builder resolving each repo's
// default-branch tip SHA "at claim time" -- but §19.9's phasing note
// assigns that exact claim-time SHA resolution, and the new
// platform-level GitHub credential it needs (the freshness pump/background
// builder has no session/creator context to borrow a token from, unlike
// this spawn-time call site), to Step 42, not this one. This Step's own
// resolved design decision, applied consistently across this file and
// app/imagebuild.Builder.attempt: a cache MISS here creates a
// best-effort, URL-only pending row (repo_urls, no built_repo_shas yet)
// and does NOTHING further -- no per-repo SHA resolution of any kind
// happens anywhere in Step 41, on the spawn path or the background pump.
// This spawn still uses the base image regardless, exactly as before.
// Step 41 lands the fingerprint/type/migration/spawn-path plumbing; Step
// 42's own claim-time resolution is what makes a brand-new fingerprint
// actually buildable end-to-end. The warm-HIT path below (a fingerprint
// that already has a 'ready' row -- e.g. seeded by whatever produces one
// once Step 42 ships) is unaffected by this boundary and works today,
// zero network calls either way.
//
// # Why this runs where it runs (not "before assembling CreateSpec")
//
// dispatch.go's own top "# Sequencing" comment establishes, and an
// adversarial review already proved empirically, that a real network call
// must NEVER hold a Postgres transaction open (planDispatch's own transact
// commits the interim Spawning claim; the real provider call always
// happens strictly AFTER that commit, in executeSpawn/executeRestore).
// Resolving a fingerprint no longer needs network access (see above), but
// it does still do a Postgres read/best-effort upsert -- so, to keep this
// function's own placement rule simple and uniform regardless of what any
// future revision of it might need, it stays in the exact same
// "outside any transaction" zone executeSpawn/executeRestore's own
// provider call already occupies -- immediately before it, not inside
// planDispatch. plan.spec.Image is an ordinary Go struct field at this
// point (not yet written to any row -- the interim Spawning claim
// committed by planDispatch never persists Image at all), so mutating it
// here, after planDispatch returns and before executeSpawn/executeRestore
// reads it, is entirely safe: ports.CreateSpec.Validate (already called
// once, inside planFreshSpawn/planRestore) only checks Gen ==
// SessionConfig.Gen, never Image, so no re-validation is needed either.
//
// # "Never block a spawn" (§10 Phase 2, the one hard invariant)
//
// Every failure branch below -- no repos, an image_builds lookup error --
// is a plain, logged, early return: plan.spec.Image is simply LEFT at
// defaultBaseImage (planFreshSpawn/planRestore's own already-committed
// choice), exactly as if this function had never been called. Nothing
// here can fail or delay executeSpawn/executeRestore's own subsequent
// call, and -- as of this Step -- nothing here can add latency either:
// every code path is a plain in-memory hash plus at most one Postgres
// round trip.

package sessionactor

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/domain/imagebuild"
)

// resolveAndSetImage implements this file's own design (see top comment).
// plan.spec.Image is mutated in place to a real image_ref ONLY when this
// exact spawn's own fingerprint already has a 'ready' image_builds row;
// every other outcome (including any error) leaves it untouched.
//
// On finding a real ready image for a NON-restore plan, this also upgrades
// plan.spec.SessionConfig.BootMode from Fresh to RepoImage
// (sessionconfig.SessionConfigBootModeRepoImage) -- see this package's own
// design-decision note in dispatch.go's doc comment / this Step's PR
// description for why: internal/domain/sandboxboot.EvaluateHook's own hook
// policy (§6.4) treats repo_image as "setup.sh already ran at build time
// and does not run again" -- reporting Fresh for a real prebuilt-image boot
// would make sandbox-agent redundantly re-run setup.sh at every boot
// regardless, defeating the entire point of image prebuilding. A restore
// plan's BootMode is deliberately left at SnapshotRestore regardless (a
// restore's own identity -- resuming from a snapshot, not a repo-image
// build -- does not change just because a matching built image also
// happens to exist for the same fingerprint).
func (a *Actor) resolveAndSetImage(ctx context.Context, plan *spawnPlan) {
	repos := plan.spec.SessionConfig.Repos
	if len(repos) == 0 {
		return // nothing to fingerprint; stays on defaultBaseImage
	}

	repoURLs := make(map[string]string, len(repos))
	for _, r := range repos {
		repoURLs[r.Name] = imagebuild.NormalizeRepoURL(r.Url)
	}

	fingerprint := imagebuild.Fingerprint(defaultBaseImage, repoURLs, a.openCodeRuntimeVersion)

	row, err := a.stores.imageBuild.Get(ctx, fingerprint)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			a.logger.Error("sessionactor: resolve image: look up image_builds failed; falling back to base image",
				"fingerprint", fingerprint, "error", err)
			return
		}
		// No row yet for this fingerprint: best-effort create a pending
		// tracking row carrying the URL-keyed fingerprint inputs, so
		// internal/app/imagebuild's own background loop has a record of
		// this repo set (see this file's own top comment for the Step
		// 41/42 boundary this best-effort row sits on: no SHA resolution
		// happens here or in that background loop yet, in Step 41). This
		// spawn still uses the base image regardless of whether the
		// upsert itself succeeds.
		a.upsertPendingImageBuildBestEffort(ctx, fingerprint, repoURLs)
		return
	}

	if row.Status == sqlcgen.ImageBuildStatusReady && row.ImageRef != nil && *row.ImageRef != "" {
		plan.spec.Image = *row.ImageRef
		if !plan.restore {
			plan.spec.SessionConfig.BootMode = sessionconfig.SessionConfigBootModeRepoImage
		}
		return
	}

	// status is pending/building/failed-but-not-yet-due: the background
	// builder's own poll query (internal/app/imagebuild's ListDueImageBuilds)
	// already matches a failed row whose next_retry_at has elapsed
	// directly, regardless of any spawn-side activity -- so nothing
	// further is written here for that case; this spawn simply falls back
	// to the base image, exactly as if no image_builds row existed at
	// all. A row already existing means the earlier "no row" branch's own
	// upsert (ON CONFLICT DO NOTHING) would be a pure no-op anyway, so it
	// is not attempted again here.
}

// upsertPendingImageBuildBestEffort best-effort inserts a fresh 'pending'
// image_builds row for fingerprint, carrying the raw (base, repoURLs,
// runtimeVersion) inputs -- the fingerprint's own URL-keyed inputs
// (migrations/000039_image_builds_shared_fingerprint.up.sql's own doc
// comment explains why those raw inputs are persisted, not just the
// fingerprint hash). Any failure (marshal, DB error/timeout) is logged
// only -- this is explicitly best-effort, never allowed to affect the
// calling spawn.
func (a *Actor) upsertPendingImageBuildBestEffort(ctx context.Context, fingerprint string, repoURLs map[string]string) {
	raw, err := json.Marshal(repoURLs)
	if err != nil {
		a.logger.Error("sessionactor: resolve image: marshal repo urls for image_builds upsert failed",
			"fingerprint", fingerprint, "error", err)
		return
	}

	if err := a.stores.imageBuild.UpsertPending(ctx, sqlcgen.UpsertPendingImageBuildParams{
		Fingerprint:    fingerprint,
		Base:           defaultBaseImage,
		RepoUrls:       raw,
		RuntimeVersion: a.openCodeRuntimeVersion,
	}); err != nil {
		a.logger.Error("sessionactor: resolve image: upsert pending image_builds row failed",
			"fingerprint", fingerprint, "error", err)
	}
}
