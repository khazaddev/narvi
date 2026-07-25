// This file (contractdrift.go) implements Step 27's ("mocking + contract
// drift", §14.3) own checkContractDrift -- the sessionactor-side half of
// the drift-detection design the user already chose: reuse Step 26's own
// per-repo GitHub-API resolution pattern, called from dispatch.go at the
// SAME post-transact hook point resolveAndSetImage (imageresolve.go)
// already occupies, scoped ONLY to sessions whose Environment is
// mock_configured=true. Unlike the reconciler/imagebuild.Builder pattern,
// this does NOT build a new periodic background ticker/job -- drift-
// checking piggybacks on existing spawn/restore events for mock-configured
// Environments only, exactly like resolveAndSetImage already piggybacks on
// them for image resolution.
//
// # Why this runs where it runs
//
// See imageresolve.go's own top comment for the full "never hold a
// Postgres transaction open across a real network call" reasoning --
// checkContractDrift is ANOTHER instance of exactly that same shape (a
// GitHub API call per repo, plus a Postgres read/best-effort upsert), so
// it lives in the SAME already-established "outside any transaction" zone
// dispatch.go's handleEnsureDispatched calls resolveAndSetImage from,
// immediately alongside it.
//
// # "Never block a spawn" (mirrors resolveAndSetImage exactly)
//
// Every early return below -- no environment_id, an environment lookup
// failure, an unscoped (non-mock-configured) Environment, no repos, a
// creator now disabled/demoted to viewer (githubtoken.go's own
// CheckCreatorGuard -- audit finding, cross-step: this check did not exist
// here at all until this fix), no usable creator GitHub token, a
// parse/API/timeout failure for any one repo, a snapshot-lookup failure --
// is a plain, logged, no-op. This function has no return value at all
// (unlike resolveAndSetImage, which mutates plan.spec.Image in place):
// checkContractDrift's only observable effects are a log line, an OTel
// counter increment, and a best-effort Postgres upsert, none of which ever
// influence whether or how a spawn proceeds.
//
// # Independent per repo, and independent of resolveAndSetImage
//
// Unlike resolveAndSetImage (which gives up on the WHOLE fingerprint the
// instant any one repo's SHA fails to resolve, since a fingerprint is
// necessarily computed over ALL of a session's repos at once), this loop
// continues past any single repo's own failure: drift-checking is a
// per-repo concern, not an all-or-nothing computation, so one repo's
// resolution failure must never suppress checking the others.
//
// This also deliberately re-resolves each repo's branch SHA via
// a.sourceControl.ResolveBranchSHA, independently of whatever
// resolveAndSetImage may have already resolved (moments earlier, for the
// same repo, in the same handleEnsureDispatched call) -- a deliberate,
// accepted simplification, at the cost of one extra GitHub API call per
// repo ONLY for mock-configured Environments (the uncommon case), that
// keeps this function fully decoupled from resolveAndSetImage's own
// control flow and failure semantics. Threading/sharing the resolved SHA
// between the two functions was considered and rejected: it would couple
// two otherwise-independent features (image prebuilding, contract-drift
// detection) through a shared intermediate value, for a savings that only
// ever applies to the rare mock-configured case.

package sessionactor

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/domain/contractdrift"
)

// checkContractDrift implements this file's own design (see top comment).
// No error return -- see that comment's "never block a spawn" section.
func (a *Actor) checkContractDrift(ctx context.Context, plan *spawnPlan) {
	if !plan.environmentID.Valid {
		// Ordinary, unscoped session -- nothing to check. The overwhelming
		// common case.
		return
	}

	env, err := a.stores.environment.Get(ctx, plan.environmentID)
	if err != nil {
		a.logger.Warn("sessionactor: check contract drift: get environment failed; skipping",
			"environment_id", plan.environmentID.String(), "error", err)
		return
	}

	if !env.MockConfigured {
		// An Environment can be scoped via pathScope alone without
		// mock_config -- drift-checking is opt-in, only for mock-configured
		// Environments (§14.3's own framing is entirely about the
		// prototyping-with-mocks workflow).
		return
	}

	repos := plan.spec.SessionConfig.Repos
	if len(repos) == 0 {
		return
	}

	if a.sourceControl == nil {
		// Defensive: mirrors resolveAndSetImage's own nil-provider guard
		// exactly (imageresolve.go) -- some tests, and any future caller
		// genuinely without one, must not panic here.
		a.logger.Warn("sessionactor: check contract drift: no SourceControl configured; skipping")
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
	// rationale. Every outcome here is a plain, logged Warn-and-skip --
	// this file's own established idiom (mirrors the env.Get/
	// contractDrift.Get failures above/below, which also Warn rather than
	// Error even for a genuine, unexpected DB failure): checkContractDrift
	// never distinguishes WHY it skipped a given spawn's drift check, only
	// THAT it did, since nothing here ever blocks or fails the spawn
	// itself either way.
	if v := CheckCreatorGuard(ctx, a.stores.user, plan.createdBy); !v.Allowed {
		switch {
		case v.Err != nil:
			a.logger.Warn("sessionactor: check contract drift: get session creator for disabled/role recheck failed; skipping",
				"error", v.Err)
		case v.Disabled:
			a.logger.Warn("sessionactor: check contract drift: session creator is now disabled; skipping (§13.3 viewer guard parity)",
				"user_id", plan.createdBy.String())
		case v.Viewer:
			a.logger.Warn("sessionactor: check contract drift: session creator is now a viewer; skipping (§13.3 viewer guard parity)",
				"user_id", plan.createdBy.String())
		}
		return
	}

	token, ok := a.decryptCreatorGitHubToken(ctx, plan.createdBy)
	if !ok {
		// Already logged by decryptCreatorGitHubToken -- same reasoning as
		// resolveAndSetImage: a creator with no usable GitHub token just
		// means no drift-checking happens, never a spawn failure.
		return
	}

	contractsPath := ""
	if env.ContractsPath != nil {
		contractsPath = *env.ContractsPath
	}

	for _, r := range repos {
		a.checkContractDriftForRepo(ctx, r, contractsPath, token)
	}
}

// checkContractDriftForRepo is checkContractDrift's own per-repo body,
// pulled out so the calling loop reads as "for each repo, do the whole
// thing" rather than a deeply-nested single function. Every failure here
// is logged and simply returns (the caller's loop continues to the next
// repo) -- see this file's own top comment for why this differs from
// resolveAndSetImage's own all-or-nothing failure handling.
func (a *Actor) checkContractDriftForRepo(ctx context.Context, r sessionconfig.SessionConfigReposElem, contractsPath, token string) {
	owner, repoName, err := parseOwnerRepo(r.Url)
	if err != nil {
		a.logger.Warn("sessionactor: check contract drift: parse owner/repo from clone url failed; skipping this repo",
			"repo", r.Name, "error", err)
		return
	}

	var branch string
	if r.Branch != nil {
		branch = *r.Branch
	}

	shaCtx, cancel := context.WithTimeout(ctx, a.timeouts.ContractsFingerprintResolutionTimeout)
	sha, resolvedBranch, err := a.sourceControl.ResolveBranchSHA(shaCtx, ports.ResolveBranchSHASpec{
		Owner: owner, Repo: repoName, Branch: branch, Token: token,
	})
	cancel()
	if err != nil {
		a.logger.Warn("sessionactor: check contract drift: resolve branch sha failed; skipping this repo",
			"repo", r.Name, "error", err)
		return
	}

	fpCtx, fpCancel := context.WithTimeout(ctx, a.timeouts.ContractsFingerprintResolutionTimeout)
	fingerprint, exists, err := a.sourceControl.ResolveContractsFingerprint(fpCtx, ports.ResolveContractsFingerprintSpec{
		Owner: owner, Repo: repoName, Ref: sha, Path: contractsPath, Token: token,
	})
	fpCancel()
	if err != nil {
		a.logger.Warn("sessionactor: check contract drift: resolve contracts fingerprint failed; skipping this repo",
			"repo", r.Name, "error", err)
		return
	}
	if !exists {
		// No contracts directory found at this repo's own configured
		// contracts path, at this ref -- the "" sentinel (migrations/
		// 000025_mock_config_contract_drift.up.sql's own doc comment).
		fingerprint = ""
	}

	// Fix (audit finding F5): repoKey includes the branch, not just
	// owner/repoName. A bare "owner/repo" key is GLOBAL across every
	// session/branch naming that repo, but the (RepoSHA, ContractsFingerprint)
	// pair this key snapshots is inherently branch-specific -- RepoSHA is
	// literally "the SHA branch currently resolves to". Two mock-configured
	// sessions on different branches of the same repo would otherwise
	// overwrite and read back each other's snapshot: session A on branch-1
	// spawns (recording branch-1's SHA), then session B on branch-2 spawns
	// and sees branch-1's SHA as "previous", with a matching fingerprint by
	// coincidence -- contractdrift.HasDrifted's own truth table then flags
	// drift even though neither branch's own contracts ever changed.
	// Uses resolvedBranch (ResolveBranchSHA's own second return), NOT the
	// local branch/r.Branch value, for this key's own branch component --
	// audit finding F5 follow-up: r.Branch is nil for a session left on
	// the repo's default branch, but that resolves (via the SAME
	// ResolveBranchSHA call above) to the exact same ref/SHA as another
	// session that explicitly names that default branch by its real name
	// (e.g. "main"). Keying on the raw, possibly-empty branch string would
	// give those two sessions two different keys ("owner/repo@" vs.
	// "owner/repo@main") for what is actually the same branch's drift
	// state -- a real drift recorded under one key would be invisible to
	// a session reading via the other, read back as a false "first
	// sighting" instead of genuine drift. resolvedBranch is never empty
	// (ResolveBranchSHA always returns the real branch name it resolved
	// SHA against, substituting the repo's actual default when spec.
	// Branch was empty), so this key is stable across nil-vs-explicit-
	// default-name sessions on the same branch.
	repoKey := owner + "/" + repoName + "@" + resolvedBranch

	var previous contractdrift.Snapshot
	row, err := a.stores.contractDrift.Get(ctx, repoKey)
	switch {
	case err == nil:
		previous = contractdrift.Snapshot{RepoSHA: row.LastRepoSha, ContractsFingerprint: row.LastContractsFingerprint}
	case errors.Is(err, pgx.ErrNoRows):
		// No snapshot recorded for this repo yet -- previous stays the
		// zero Snapshot{} (RepoSHA == ""), contractdrift.HasDrifted's own
		// "first sighting" case.
	default:
		// Don't overwrite state we couldn't safely read.
		a.logger.Warn("sessionactor: check contract drift: get previous snapshot failed; skipping this repo",
			"repo", repoKey, "error", err)
		return
	}

	current := contractdrift.Snapshot{RepoSHA: sha, ContractsFingerprint: fingerprint}

	if contractdrift.HasDrifted(previous, current) {
		a.logger.Warn("sessionactor: contract drift detected",
			"repo", repoKey, "previous_sha", previous.RepoSHA, "current_sha", current.RepoSHA)
		a.contractDriftDetected.Add(ctx, 1)
	}

	// Best-effort: persist the latest snapshot for next time, regardless
	// of whether drift was detected this round.
	if err := a.stores.contractDrift.Upsert(ctx, repoKey, current.RepoSHA, current.ContractsFingerprint); err != nil {
		a.logger.Error("sessionactor: check contract drift: upsert snapshot failed",
			"repo", repoKey, "error", err)
	}
}
