package gitclone

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
	"github.com/khazaddev/narvi/internal/domain/environment"
	"github.com/khazaddev/narvi/internal/domain/gitstate"
	"github.com/khazaddev/narvi/internal/domain/reposource"
	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/internal/sandboxagent/supervisor"
)

// OnGitSync is called by SyncAll immediately before each real stash/
// checkout/pop phase begins for one repo, so the caller
// (cmd/sandbox-agent/main.go) can translate it into an outbound
// sandboxws.GitSync event without this package needing to know anything
// about wsbridge/wire types -- mirroring how internal/sandboxagent/
// services.ProgressReporter already decouples a lower-level package from
// wire-type knowledge for boot_progress. status is always one of "stash",
// "checkout", "pop" (matching sandboxws.GitSyncStatus's three wire values
// exactly); branch is the resolved (gitstate.ResolveSessionBranch),
// already-normalized target branch for this repo.
type OnGitSync func(repoName, status, branch string)

// SyncResult is one repo's outcome from SyncAll.
type SyncResult struct {
	Repo sessionconfig.SessionConfigReposElem
	// Primary is true for exactly the repo at position 0 (§3.4: "position 0
	// = primary"), mirroring CloneResult.Primary exactly.
	Primary bool
	// Dir is workspaceDir/<Repo.Name> -- set even when Err is non-nil, with
	// the same one exception CloneResult.Dir documents: a Repo.Name/Url/
	// Branch that fails reposource validation leaves Dir as the empty
	// string, never joined against an unvalidated Name.
	Dir string
	// Branch is the resolved target branch (gitstate.ResolveSessionBranch)
	// this repo's checkout step targeted -- "" if validation failed before
	// a branch was ever resolved.
	Branch string
	// State is the boot machine's own final state for this repo
	// (internal/domain/gitstate): gitstate.StateReady on success, one of
	// the four terminal failure states otherwise, or the zero State
	// (StateIdle) if this repo never even entered the machine (a
	// validation failure, or a real `git status` failure that made it
	// impossible to tell whether the tree was even dirty).
	// gitstate.RequiresStashRecovery(State) is the P0 check: true means a
	// stash exists in this repo's own stash list, unpopped, requiring
	// manual recovery.
	State gitstate.State
	// Err is non-nil if this repo's reconciliation failed at any step.
	Err error
}

// ToCloneResult adapts one SyncResult into the CloneResult shape
// WriteAgentsManifest already renders (§6.4) -- cmd/sandbox-agent's own
// runBootSequence calls WriteAgentsManifest after EITHER CloneAll (a fresh
// clone) or SyncAll (reconciling an already-existing workspace), so both
// paths need to produce the same manifest input shape. Repo.Branch is
// overridden to point at r.Branch (the ACTUAL resolved/checked-out branch,
// which may be a freshly-invented "narvi/<sessionID>" name when
// repos[].branch was nil) whenever this repo synced successfully --
// strictly more useful in the generated manifest than CloneResult.Repo.
// Branch's own "nil means render (default)" convention, since SyncAll (unlike
// CloneAll) always resolves a concrete branch one way or another.
func (r SyncResult) ToCloneResult() CloneResult {
	repo := r.Repo
	if r.Err == nil && r.Branch != "" {
		branch := r.Branch
		repo.Branch = &branch
	}
	return CloneResult{Repo: repo, Primary: r.Primary, Dir: r.Dir, Err: r.Err}
}

// SyncAll reconciles every repo in repos, IN ORDER, against an
// ALREADY-EXISTING workspace (§3.4's boot-time "stash-if-dirty -> checkout
// session branch (create from base if absent) -> stash pop" sequence) --
// the counterpart to CloneAll for a BootModeRepoImage/
// BootModeSnapshotRestore boot, whose workspace already has a real git
// repo on disk (baked into the image or restored from a snapshot) rather
// than an empty directory waiting to be cloned into. SyncAll never runs
// `git clone` -- cloning again into a non-empty directory would conflict
// with what is already there.
//
// Per repo, this is the piece internal/domain/gitstate's own doc.go names
// as a later Step's job: actually running git and feeding each real
// outcome back into that package's pure Transition table, via the
// TriggerFor* helpers (sequence.go) it exists specifically for. onGitSync
// fires once per phase actually entered (stash only if the tree was dirty;
// pop only if a stash was actually taken), each carrying this repo's own
// name/status/resolved branch -- it does NOT gate or block on the caller's
// own handling; SyncAll always proceeds to the next phase regardless (§3.4
// git_sync is a best-effort, non-critical wire event with no ack).
//
// Criticality mirrors CloneAll exactly (§3.4: "position 0 = primary"): a
// primary repo's failure is fatal and stops the loop immediately -- no
// repo after it is even attempted; a secondary repo's failure is logged as
// a warning and the loop continues. Regardless of primary/secondary, a
// failure that leaves gitstate.RequiresStashRecovery true (a stash
// conflicted with checkout, or the final pop itself failed) is ALWAYS
// additionally logged loudly at Error level -- §3.4's own P0: "User
// working-tree edits are durable data -- losing them is a P0" -- but never
// crashes this process; results always reflects every repo actually
// attempted (in order), regardless of outcome.
//
// pathScope (§14.1) is re-applied here too, exactly like CloneAll does for
// a fresh clone -- this is NOT redundant on a BootModeRepoImage/
// BootModeSnapshotRestore boot: a repo_image's fingerprint/ImageSpec is
// (base, repoSHAs, runtimeVersion) only, scope-independent, so the SAME
// prebuilt image (or restored snapshot) can be shared across sessions with
// different path_scope values, or none at all. The on-disk sparse-checkout
// state SyncAll finds reflects whatever scope (or lack of one) happened to
// produce that image/snapshot -- NOT necessarily THIS session's own scope.
// SyncAll is therefore the enforcement point that reconciles the two:
// pathScope is validated (internal/domain/environment.ValidatePathScope)
// ONCE, before any repo is even attempted, exactly like CloneAll, and a
// non-empty pathScope re-narrows every repo's sparse-checkout
// configuration (applySparseCheckout, clone.go) once that repo itself
// reaches gitstate.StateReady.
func SyncAll(
	ctx context.Context,
	sup *supervisor.Supervisor,
	workspaceDir string,
	repos []sessionconfig.SessionConfigReposElem,
	pathScope []string,
	sessionID string,
	stepTimeout, stopGrace time.Duration,
	onGitSync OnGitSync,
) ([]SyncResult, error) {
	if len(repos) == 0 {
		return nil, nil
	}

	if err := environment.ValidatePathScope(pathScope); err != nil {
		return nil, fmt.Errorf("gitclone: invalid path scope: %w", err)
	}

	results := make([]SyncResult, 0, len(repos))
	for i, repo := range repos {
		primary := i == 0

		result := syncOne(ctx, sup, workspaceDir, repo, primary, pathScope, sessionID, stepTimeout, stopGrace, onGitSync)
		results = append(results, result)

		if result.Err == nil {
			continue
		}

		if primary {
			return results, fmt.Errorf("gitclone: primary repo %q failed to sync (fatal): %w", repo.Name, result.Err)
		}
		platform.Logger(ctx).Warn("gitclone: secondary repo failed to sync, continuing",
			"repo", repo.Name, "error", result.Err)
	}

	return results, nil
}

// syncOne implements SyncAll's own per-repo body -- pulled out so the
// calling loop reads as "for each repo, do the whole thing" rather than a
// deeply-nested single function, mirroring internal/app/sessionactor/
// contractdrift.go's own checkContractDriftForRepo precedent for the same
// reason.
func syncOne(
	ctx context.Context,
	sup *supervisor.Supervisor,
	workspaceDir string,
	repo sessionconfig.SessionConfigReposElem,
	primary bool,
	pathScope []string,
	sessionID string,
	stepTimeout, stopGrace time.Duration,
	onGitSync OnGitSync,
) (result SyncResult) {
	// Validate BEFORE any filepath.Join or sup.Spawn happens for this repo
	// -- same reasoning, same helper (validateRepoSpec, clone.go), as
	// CloneAll's own identical guard: repo.Name/Url/Branch are all
	// session-controlled and reach this loop with no upstream validation of
	// their own.
	if err := validateRepoSpec(repo); err != nil {
		return SyncResult{Repo: repo, Primary: primary, Err: err}
	}

	dir := filepath.Join(workspaceDir, repo.Name)

	branch := gitstate.ResolveSessionBranch(repo.Branch, sessionID)
	// The RESOLVED branch (§3.4's own invented "narvi/<sessionID>" case,
	// branchname.go) has never itself been validated -- validateRepoSpec
	// above only checked repo.Branch verbatim, when non-nil. Re-validating
	// the resolved value uniformly covers both the explicit-branch and the
	// generated-branch case with the SAME rule, before it ever reaches a
	// git subprocess's argument list, mirroring this package's own
	// "validate before every new git invocation" convention exactly.
	if err := reposource.ValidateBranch(branch); err != nil {
		return SyncResult{Repo: repo, Primary: primary, Dir: dir, Branch: branch,
			Err: fmt.Errorf("gitclone: invalid resolved branch %q: %w", branch, err)}
	}

	result = SyncResult{Repo: repo, Primary: primary, Dir: dir, Branch: branch}

	// pathScope (§14.1) re-narrowing is attempted on EVERY exit from this
	// point forward, via this deferred call -- regardless of whether the
	// stash/checkout/pop sequence below succeeds, or fails at ANY step
	// (git status, stash push, checkout, or stash pop). This closes a
	// residual instance of the exact bypass §14.1 exists to prevent: a
	// repo_image/snapshot_restore boot's workspace may already contain a
	// full, unscoped checkout baked at image time (see SyncAll's own doc
	// comment on why re-applying scope here is not redundant), and a
	// secondary repo's failure is only ever logged as a warning
	// (SyncAll's own primary-fatal/secondary-warn split) -- the sandbox
	// still boots and becomes reachable regardless. Leaving that repo's
	// on-disk scope unenforced merely because ITS OWN git-sync sequence
	// happened to error out before reaching StateReady would silently
	// reopen the bypass for that repo's directory. This is deliberately
	// best-effort and additive: it never clears an earlier, more specific
	// failure already recorded in result.Err (it only appends a
	// sparse-checkout failure on top of one, if any), and it never touches
	// result.State, which keeps reporting whichever gitstate.State the
	// stash/checkout/pop sequence itself reached -- exactly as before this
	// change.
	if len(pathScope) > 0 {
		defer func() {
			sparseErr := applySparseCheckout(ctx, sup, dir, pathScope, stepTimeout, stopGrace)
			if sparseErr == nil {
				return
			}
			wrapped := fmt.Errorf("gitclone: sparse-checkout %s: %w", repo.Name, sparseErr)
			if result.Err != nil {
				result.Err = fmt.Errorf("%w (repo also failed to sync earlier: %v)", wrapped, result.Err)
			} else {
				result.Err = wrapped
			}
		}()
	}

	state := gitstate.StateIdle

	dirty, err := gitStatusDirty(ctx, sup, dir, stepTimeout, stopGrace)
	if err != nil {
		result.Err = fmt.Errorf("gitclone: git status for %s: %w", repo.Name, err)
		return result
	}

	// Legal from StateIdle by construction: TriggerForDirtyCheck only ever
	// returns TriggerDirtyDetected or TriggerCleanDetected, both edges
	// Idle's own transitions table entry defines (state.go) -- an error
	// here would mean this package and gitstate have drifted out of sync,
	// not a real, reachable failure.
	state, err = gitstate.Transition(state, gitstate.TriggerForDirtyCheck(dirty))
	if err != nil {
		result.Err = fmt.Errorf("gitclone: %s: unexpected transition error: %w", repo.Name, err)
		return result
	}

	if dirty {
		onGitSync(repo.Name, "stash", branch)
		stashErr := gitStashPush(ctx, sup, dir, stepTimeout, stopGrace)
		state, _ = gitstate.Transition(state, gitstate.TriggerForStash(stashErr == nil))
		if state == gitstate.StateStashFailed {
			result.State = state
			result.Err = fmt.Errorf("gitclone: stash push for %s: %w", repo.Name, stashErr)
			return result
		}
	}

	onGitSync(repo.Name, "checkout", branch)
	checkoutErr := checkoutBranch(ctx, sup, dir, branch, stepTimeout, stopGrace)
	// Checked directly against checkoutErr, not gitstate.IsTerminal(state):
	// a SUCCESSFUL checkout also lands in a state IsTerminal reports true
	// for whenever the tree was clean (StateReady itself is one of the
	// five terminal states, success included) -- checkoutErr == nil is the
	// unambiguous "did this step itself fail" signal; state only decides
	// WHICH terminal-failure state to report, via Transition above.
	state, _ = gitstate.Transition(state, gitstate.TriggerForCheckout(checkoutErr == nil))
	if checkoutErr != nil {
		result.State = state
		result.Err = fmt.Errorf("gitclone: checkout %s for %s: %w", branch, repo.Name, checkoutErr)
		logIfStashRecoveryNeeded(ctx, repo.Name, dir, branch, state)
		return result
	}

	if state == gitstate.StatePoppingStash {
		onGitSync(repo.Name, "pop", branch)
		popErr := gitStashPop(ctx, sup, dir, stepTimeout, stopGrace)
		state, _ = gitstate.Transition(state, gitstate.TriggerForPop(popErr == nil))
		if state == gitstate.StatePopFailed {
			result.State = state
			result.Err = fmt.Errorf("gitclone: stash pop for %s: %w", repo.Name, popErr)
			logIfStashRecoveryNeeded(ctx, repo.Name, dir, branch, state)
			return result
		}
	}

	// state is StateReady here: either the tree was clean and checkout
	// succeeded directly, or it was dirty, checkout succeeded, and the pop
	// above (whether or not this repo ever took that branch) succeeded too.
	// pathScope re-narrowing itself already happens unconditionally via the
	// deferred call above, regardless of this success path or any earlier
	// failure -- see its own comment for why.
	result.State = state
	return result
}

// logIfStashRecoveryNeeded logs, at Error level, exactly the P0 case §3.4
// exists to make impossible to miss: a stash sits in repo's own stash
// list, unpopped, holding real user edits. Called from every failure exit
// in syncOne -- a no-op (via gitstate.RequiresStashRecovery's own false
// result) for the two terminal failures that never took a stash
// (StashFailed, CheckoutFailedClean).
func logIfStashRecoveryNeeded(ctx context.Context, repoName, dir, branch string, state gitstate.State) {
	if !gitstate.RequiresStashRecovery(state) {
		return
	}
	platform.Logger(ctx).Error(
		"gitclone: git-sync left a stash outstanding -- manual recovery required (P0: user working-tree edits are sitting in `git stash list`, unpopped)",
		"repo", repoName, "dir", dir, "branch", branch, "state", string(state),
	)
}

// runGit runs `git <args...>` (already fully built, including any -C <dir>
// prefix the caller supplies) via sup (never a bare exec.Command), bounded
// by stepTimeout, and returns its trimmed stdout -- mirroring cloneOne's
// own exact timeout/stop/error-wrapping shape (clone.go) precisely: a hang
// is stopped (bounded by stopGrace, using the OUTER ctx for the Stop call,
// not the already-expired step-scoped context) and reported as a timeout
// failure; a non-zero exit or a wait failure is likewise a real, returned
// error.
func runGit(ctx context.Context, sup *supervisor.Supervisor, args []string, stepTimeout, stopGrace time.Duration) (string, error) {
	var stdout bytes.Buffer
	proc, err := sup.Spawn(supervisor.Spec{
		Path:   "git",
		Args:   args,
		Stdout: &stdout,
	})
	if err != nil {
		return "", fmt.Errorf("spawn git %s: %w", strings.Join(args, " "), err)
	}

	stepCtx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()

	result, waitErr := proc.Wait(stepCtx)
	if waitErr != nil {
		_ = proc.Stop(ctx, stopGrace)
		return "", fmt.Errorf("git %s: did not complete within %s: %w", strings.Join(args, " "), stepTimeout, waitErr)
	}
	if result.Err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), result.Err)
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("git %s: exited %d", strings.Join(args, " "), result.ExitCode)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// gitStatusDirty runs `git -C <dir> status --porcelain` and reports
// whether the working tree has any uncommitted change (§3.4:
// "stash-if-dirty"). Any output at all (even a single line) means dirty;
// empty output means clean. A real command failure (e.g. dir is not a git
// repository) is returned as an error, distinct from "clean" -- this repo
// never even reaches gitstate.Transition in that case (see syncOne).
func gitStatusDirty(ctx context.Context, sup *supervisor.Supervisor, dir string, stepTimeout, stopGrace time.Duration) (bool, error) {
	out, err := runGit(ctx, sup, []string{"-C", dir, "status", "--porcelain"}, stepTimeout, stopGrace)
	if err != nil {
		return false, err
	}
	return out != "", nil
}

// gitStashPush runs `git -C <dir> stash push --include-untracked`, called
// only when gitStatusDirty already reported a dirty tree (§3.4:
// stash-if-dirty). --include-untracked is load-bearing, not optional:
// gitStatusDirty runs `git status --porcelain`, which reports an untracked
// file as dirty output exactly like a tracked modification does, so a tree
// that is "dirty" ONLY because of a brand-new untracked file must still
// produce a real stash entry here. Without this flag, plain `git stash
// push` has NOTHING to stash in that case (untracked files are not stash
// candidates by default) and exits 0 with "No local changes to save" --
// stashErr == nil, so the state machine advances to
// StateCheckingOutWithStash believing a stash was taken, and the later
// unconditional `git stash pop` then fails with "no stash entries found"
// (StatePopFailed), triggering a spurious P0 "stash outstanding" log and a
// fatal sync failure even though nothing was ever at risk and `git stash
// list` is genuinely empty. --include-untracked closes this by actually
// moving the untracked file into the stash (matching what gitStatusDirty
// already decided counts as "dirty"), so the pop below has a real entry to
// restore.
func gitStashPush(ctx context.Context, sup *supervisor.Supervisor, dir string, stepTimeout, stopGrace time.Duration) error {
	_, err := runGit(ctx, sup, []string{"-C", dir, "stash", "push", "--include-untracked"}, stepTimeout, stopGrace)
	return err
}

// gitStashPop runs `git -C <dir> stash pop --index`, called only when a
// stash was actually taken (state == StatePoppingStash) and checkout onto
// the session branch already succeeded. --index is load-bearing: without
// it, plain `git stash pop` restores stashed content into the working tree
// but always leaves it unstaged, even if it was `git add`-ed (staged) at
// stash time -- a real, verified divergence from "edits survive
// byte-for-byte" once staging state is counted as part of the edit.
// --index asks git to also restore the index, so a staged-but-uncommitted
// change round-trips through stash/checkout/pop still staged, not just
// content-identical. A failure here (e.g. the stash conflicts with the
// newly-checked-out branch, or --index itself cannot cleanly reapply) is
// the P0 case §3.4 exists to make impossible to miss: verified directly
// against real git behavior (sync_test.go) that a failed `stash pop`
// leaves the stash entry in place in the stash list -- git itself never
// drops it on a conflict -- so nothing is silently lost even on this path,
// only left for a human to resolve.
func gitStashPop(ctx context.Context, sup *supervisor.Supervisor, dir string, stepTimeout, stopGrace time.Duration) error {
	_, err := runGit(ctx, sup, []string{"-C", dir, "stash", "pop", "--index"}, stepTimeout, stopGrace)
	return err
}

// branchExistsLocally runs `git -C <dir> rev-parse --verify --quiet
// refs/heads/<branch>`, exit-code based, never string-matching output:
// verified directly against real git behavior (sync_test.go) that exit 0
// means the local branch exists and exit 1 means it does not -- both
// expected, valid outcomes, not error paths. Any OTHER exit code (a
// genuinely broken repo, an unexpected git failure) is returned as a real
// error. refs/heads/ is prefixed explicitly so this checks for a LOCAL
// branch specifically, never a coincidentally-matching tag or
// remote-tracking ref.
func branchExistsLocally(ctx context.Context, sup *supervisor.Supervisor, dir, branch string, stepTimeout, stopGrace time.Duration) (bool, error) {
	proc, err := sup.Spawn(supervisor.Spec{
		Path: "git",
		Args: []string{"-C", dir, "rev-parse", "--verify", "--quiet", "refs/heads/" + branch},
	})
	if err != nil {
		return false, fmt.Errorf("spawn git rev-parse --verify: %w", err)
	}

	stepCtx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()

	result, waitErr := proc.Wait(stepCtx)
	if waitErr != nil {
		_ = proc.Stop(ctx, stopGrace)
		return false, fmt.Errorf("git rev-parse --verify: did not complete within %s: %w", stepTimeout, waitErr)
	}
	if result.Err != nil {
		return false, fmt.Errorf("git rev-parse --verify: %w", result.Err)
	}
	switch result.ExitCode {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		return false, fmt.Errorf("git rev-parse --verify: exited %d", result.ExitCode)
	}
}

// checkoutBranch checks out branch in dir, creating it from HEAD (§3.4:
// "create from base if absent") if it does not already exist as a local
// branch. branch is placed as the LAST positional argument, followed by a
// trailing "--", in BOTH forms -- verified directly against the real git
// binary (sync_test.go), not assumed: `git checkout <branch> --` and `git
// checkout -b <branch> HEAD --` both switch/create the branch exactly as
// expected, since a trailing "--" with nothing after it means "no
// pathspecs follow" without changing branch's own role as the preceding
// positional. This is deliberately NOT `git checkout -- <branch>` (a
// LEADING "--"): verified directly (sync_test.go) that placement instead
// makes git treat branch as a PATHSPEC to restore, not a ref to switch
// to -- the opposite of what this function needs. reposource.ValidateBranch
// (already run on branch by syncOne before this is ever called) is this
// codebase's own primary defense against a "-"-prefixed branch value being
// misread as a flag in the first place; the trailing "--" here is
// additional defense in depth, mirroring cloneOne's own "-- before every
// positional" convention as closely as checkout's own real semantics
// allow.
func checkoutBranch(ctx context.Context, sup *supervisor.Supervisor, dir, branch string, stepTimeout, stopGrace time.Duration) error {
	exists, err := branchExistsLocally(ctx, sup, dir, branch, stepTimeout, stopGrace)
	if err != nil {
		return fmt.Errorf("determine whether branch %s exists: %w", branch, err)
	}

	args := []string{"-C", dir, "checkout"}
	if exists {
		args = append(args, branch, "--")
	} else {
		args = append(args, "-b", branch, "HEAD", "--")
	}

	_, err = runGit(ctx, sup, args, stepTimeout, stopGrace)
	return err
}

// CleanForImageBuild runs, for each repo name IN ORDER, `git -C <dir>
// clean -fdx` (discard untracked/ignored residue) followed by `git -C
// <dir> checkout -- .` (discard any tracked-file modifications) -- §3.4:
// "Image builds must snapshot a clean tree (commit or clean setup.sh
// residue before snapshotting)". DISCARDING is the choice made here, one
// of §3.4's own explicit either/or options ("commit OR clean"): setup.sh
// residue (installed dependencies, caches, generated files) is
// overwhelmingly likely to be untracked/gitignored build output, not
// meaningful source changes worth preserving, so there is nothing here
// worth a real commit.
//
// Called only for a BootModeBuild boot (cmd/sandbox-agent/main.go's own
// runBootSequence), AFTER hooks/setup.sh have already completed
// successfully -- a failed setup.sh in build mode is already fatal per
// BootModeBuild's own existing primary-fatal semantics (sandboxboot.
// EvaluateHook), so this function is never reached on that path at all. A
// failure HERE is treated with that SAME fatal semantics, matching it
// exactly: this loop stops and returns the first error immediately,
// unlike CloneAll/SyncAll's own primary-fatal/secondary-warn split -- a
// workspace this function fails to clean is not safe to snapshot at all,
// regardless of which repo (primary or secondary) it was.
//
// Every name in repoNames is validated (reposource.ValidateRepoName)
// BEFORE any filepath.Join happens for it -- the same guarantee
// validateRepoSpec documents for CloneAll/SyncAll, applied here directly
// since this function is EXPORTED and takes bare repo names rather than
// sessionconfig.SessionConfigReposElem values already run through
// validateRepoSpec upstream. Today's only call site (cmd/sandbox-agent/
// main.go's runBootSequence) happens to supply names already validated by
// an earlier CloneAll/SyncAll, but this function does not rely on that --
// a repo name containing ".." would otherwise let `git -C <dir> clean
// -fdx` / `git -C <dir> checkout -- .` run against a directory OUTSIDE
// workspaceDir entirely, destroying untracked files there (verified
// directly against the real git binary: this is not theoretical).
func CleanForImageBuild(ctx context.Context, sup *supervisor.Supervisor, workspaceDir string, repoNames []string, timeout, stopGrace time.Duration) error {
	for _, name := range repoNames {
		if err := reposource.ValidateRepoName(name); err != nil {
			return fmt.Errorf("gitclone: invalid repo name %q before image-build clean (fatal): %w", name, err)
		}

		dir := filepath.Join(workspaceDir, name)

		if _, err := runGit(ctx, sup, []string{"-C", dir, "clean", "-fdx"}, timeout, stopGrace); err != nil {
			return fmt.Errorf("gitclone: clean %s before snapshot (fatal): %w", name, err)
		}
		if _, err := runGit(ctx, sup, []string{"-C", dir, "checkout", "--", "."}, timeout, stopGrace); err != nil {
			return fmt.Errorf("gitclone: discard tracked modifications in %s before snapshot (fatal): %w", name, err)
		}
	}
	return nil
}
