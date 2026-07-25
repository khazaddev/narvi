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
// # Why this runs where it runs (not "before assembling CreateSpec")
//
// dispatch.go's own top "# Sequencing" comment establishes, and an
// adversarial review already proved empirically, that a real network call
// must NEVER hold a Postgres transaction open (planDispatch's own transact
// commits the interim Spawning claim; the real provider call always
// happens strictly AFTER that commit, in executeSpawn/executeRestore).
// Resolving a fingerprint is ALSO real, network-bound work (a GitHub API
// call per repo, plus a Postgres read/best-effort upsert) -- so it must
// obey the exact same rule. planFreshSpawn/planRestore build their
// CreateSpec INSIDE that same transact (their own doc comments), which
// means image resolution cannot happen there either.
//
// The fix: resolveAndSetImage runs in the SAME already-established
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
// Every failure branch below -- no repos, a creator now disabled/demoted
// to viewer (githubtoken.go's own CheckCreatorGuard -- audit finding,
// cross-step: this check did not exist here at all until this fix), no
// usable creator GitHub token, a parse/API/timeout failure for ANY one
// repo, an image_builds lookup error -- is a plain, logged, early return:
// plan.spec.Image is simply LEFT at defaultBaseImage (planFreshSpawn/
// planRestore's own already-committed choice), exactly as if this
// function had never been called.
// Nothing here can fail or delay executeSpawn/executeRestore's own
// subsequent call. The one deliberate, bounded exception is LATENCY, not
// blocking: when a session names real repos, this DOES add real,
// sequential wall-clock time to a spawn attempt (at most
// len(repos)*platform.Timeouts.RepoSHAResolutionTimeout for the GitHub
// calls, plus one Postgres round trip) -- a deliberate, explicitly bounded
// trade-off for real per-session image selection, not the unbounded/
// indefinite "block" §10 Phase 2 prohibits (which is about never WAITING
// on a slow BuildImage call itself -- that always happens later,
// asynchronously, in internal/app/imagebuild's own background loop, never
// synchronously during a spawn).

package sessionactor

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/ports"
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

	if a.sourceControl == nil {
		// Defensive: mirrors tryPlanSpawn's own nil-provider guard and
		// tryPlanDispatch's own nil-commander guard exactly (dispatch.go)
		// -- some tests, and any future caller genuinely without one, must
		// not panic here. Falls back to the base image, exactly like every
		// other early-return in this function.
		a.logger.Warn("sessionactor: resolve image: no SourceControl configured; falling back to base image")
		return
	}

	// Creator disabled/role recheck (audit finding, cross-step: this
	// function minted and used the session creator's real GitHub token
	// against the live GitHub API below with NO recheck at all -- the
	// exact gap githubtoken.go's own CheckCreatorGuard exists to close,
	// already fixed for pushpr.go's createPRBestEffort and
	// scmcredentials.go's ScmCredentials). Checked fresh, right here,
	// before ever decrypting/using the creator's token below -- see
	// CheckCreatorGuard's own doc comment for the complete staleness
	// rationale. Falls back to the base image on any outcome here,
	// exactly like every other early return in this function -- a genuine
	// GetByID failure logs at Error (mirroring this function's own
	// imageBuild.Get handling just below: Error for an unexpected DB
	// failure, distinct from an expected miss), Disabled/Viewer log at
	// Warn (an expected, security-relevant skip, not a malfunction).
	if v := CheckCreatorGuard(ctx, a.stores.user, plan.createdBy); !v.Allowed {
		switch {
		case v.Err != nil && !v.ErrNotFound:
			a.logger.Error("sessionactor: resolve image: get session creator for disabled/role recheck failed; falling back to base image",
				"error", v.Err)
		case v.Err != nil:
			a.logger.Warn("sessionactor: resolve image: session creator row not found for disabled/role recheck; falling back to base image",
				"user_id", plan.createdBy.String())
		case v.Disabled:
			a.logger.Warn("sessionactor: resolve image: session creator is now disabled; falling back to base image (§13.3 viewer guard parity)",
				"user_id", plan.createdBy.String())
		case v.Viewer:
			a.logger.Warn("sessionactor: resolve image: session creator is now a viewer; falling back to base image (§13.3 viewer guard parity)",
				"user_id", plan.createdBy.String())
		}
		return
	}

	token, ok := a.decryptCreatorGitHubToken(ctx, plan.createdBy)
	if !ok {
		// Already logged by decryptCreatorGitHubToken. This is also the
		// documented reason a session whose creator has no usable GitHub
		// token (never linked GitHub, no stored token, or a decrypt
		// failure) still spawns successfully on the base image -- never
		// blocked or failed by this mechanism.
		return
	}

	repoSHAs := make(map[string]string, len(repos))
	for _, r := range repos {
		owner, repoName, err := parseOwnerRepo(r.Url)
		if err != nil {
			a.logger.Warn("sessionactor: resolve image: parse owner/repo from clone url failed; falling back to base image",
				"repo", r.Name, "error", err)
			return
		}

		var branch string
		if r.Branch != nil {
			branch = *r.Branch
		}

		shaCtx, cancel := context.WithTimeout(ctx, a.timeouts.RepoSHAResolutionTimeout)
		sha, _, err := a.sourceControl.ResolveBranchSHA(shaCtx, ports.ResolveBranchSHASpec{
			Owner: owner, Repo: repoName, Branch: branch, Token: token,
		})
		cancel()
		if err != nil {
			a.logger.Warn("sessionactor: resolve image: resolve branch sha failed; falling back to base image",
				"repo", r.Name, "error", err)
			return
		}
		repoSHAs[r.Name] = sha
	}

	fingerprint := imagebuild.Fingerprint(defaultBaseImage, repoSHAs, a.openCodeRuntimeVersion)

	row, err := a.stores.imageBuild.Get(ctx, fingerprint)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			a.logger.Error("sessionactor: resolve image: look up image_builds failed; falling back to base image",
				"fingerprint", fingerprint, "error", err)
			return
		}
		// No row yet for this fingerprint: best-effort create a pending
		// tracking row so internal/app/imagebuild's own background loop
		// picks it up on a later tick. This spawn still uses the base
		// image regardless of whether the upsert itself succeeds.
		a.upsertPendingImageBuildBestEffort(ctx, fingerprint, repoSHAs)
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
// image_builds row for fingerprint, carrying the raw (base, repoSHAs,
// runtimeVersion) inputs the background builder needs to actually attempt
// a build later (migrations/000024_image_builds.up.sql's own doc comment
// explains why those raw inputs are persisted, not just the fingerprint
// hash). Any failure (marshal, DB error/timeout) is logged only -- this is
// explicitly best-effort, never allowed to affect the calling spawn.
func (a *Actor) upsertPendingImageBuildBestEffort(ctx context.Context, fingerprint string, repoSHAs map[string]string) {
	raw, err := json.Marshal(repoSHAs)
	if err != nil {
		a.logger.Error("sessionactor: resolve image: marshal repo shas for image_builds upsert failed",
			"fingerprint", fingerprint, "error", err)
		return
	}

	if err := a.stores.imageBuild.UpsertPending(ctx, sqlcgen.UpsertPendingImageBuildParams{
		Fingerprint:    fingerprint,
		Base:           defaultBaseImage,
		RepoShas:       raw,
		RuntimeVersion: a.openCodeRuntimeVersion,
	}); err != nil {
		a.logger.Error("sessionactor: resolve image: upsert pending image_builds row failed",
			"fingerprint", fingerprint, "error", err)
	}
}
