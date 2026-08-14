package boot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/khazaddev/narvi/internal/domain/sandboxboot"
	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/internal/sandboxagent/supervisor"
)

// workspaceMovedFor looks up repoName in moved (§19.4's per-repo
// workspaceMoved map, boot.ComputeWorkspaceMoved) -- a repo genuinely
// absent from the map (e.g. moved was nil, the common case for every mode
// other than repo_image, which never computes one at all -- see
// runBootSequence in cmd/sandbox-agent/main.go) defaults to true, the SAME
// safe "assume moved" default ComputeWorkspaceMoved itself documents for a
// missing/unreadable manifest -- never silently defaults to false (which
// would silently reopen exactly the "missing dependency" gap §19.4 exists
// to close).
func workspaceMovedFor(moved map[string]bool, repoName string) bool {
	v, ok := moved[repoName]
	if !ok {
		return true
	}
	return v
}

// ladderFor looks up repoName in ladder (§19.6's per-repo
// SetupRerunLadder map, boot.ComputeSetupRerunLadder) -- a repo genuinely
// absent from the map (ladder itself nil, the common case for every call
// site that predates this Step or never bothered computing one -- every
// existing test in this package, plus every mode other than repo_image)
// defaults to the SAME conservative floor ComputeSetupRerunLadder itself
// produces for a missing manifest: DependencySkipIneligible and
// DeltaEligible: false, i.e. "fall all the way through to full setup.sh" --
// today's exact pre-Step-43 behavior, never a spurious skip or a
// spuriously-preferred delta script.
func ladderFor(ladder map[string]SetupRerunLadder, repoName string) SetupRerunLadder {
	v, ok := ladder[repoName]
	if !ok {
		return SetupRerunLadder{DependencySkip: DependencySkipIneligible, DeltaEligible: false}
	}
	return v
}

// RepoInfo is one repo's boot-hook-relevant identity: its directory name
// under WorkspaceDir (i.e. /workspace/{Name}), and whether it's the
// primary repo (§3.4: "position 0 = primary" in SESSION_CONFIG.repos). The
// CALLER (a later Step) is responsible for setting Primary correctly when
// it builds the []RepoInfo slice from a real SessionConfig -- this Step's
// own tests cover both primary=true and primary=false directly, without
// needing a real SessionConfig.
type RepoInfo struct {
	Name    string
	Primary bool
}

// RunHooks runs, for each repo IN ORDER, HookSetup then HookStart (in that
// order), per §6.4's hook policy (sandboxboot.EvaluateHook):
//
//   - !ShouldRun: skipped entirely, no log -- this is routine.
//   - ShouldRun but the script is absent on disk: skipped silently -- a
//     repo without a start.sh/setup.sh is normal and expected, not an
//     error or even a warning.
//   - ShouldRun and present: spawned directly via sup (Dir =
//     workspaceDir/repo.Name, Path = the script's absolute path -- run
//     directly as an executable, never wrapped in "sh -c"; the script
//     itself is expected to carry a shebang), waited on bounded by
//     hookTimeout; a hook still running when that bound expires is
//     stopped (bounded by stopGrace) and the timeout itself is treated as
//     a failure.
//   - On any failure (non-zero exit OR timeout): if FatalOnFailure, RunHooks
//     returns the error immediately, running no further hooks or repos. If
//     not fatal, it logs a warning via platform.Logger(ctx) and continues
//     to the next hook/repo.
//
// An empty repos slice is a correct, immediate no-op (returns nil).
//
// workspaceMoved (§19.4, Step 42) is the per-repo predicate
// boot.ComputeWorkspaceMoved computes once per boot from
// /narvi/image-manifest.json -- consulted by sandboxboot.EvaluateHook for
// exactly one cell (repo_image + HookSetup). nil is a correct, safe input
// (every entry defaults to "moved", via workspaceMovedFor) -- the shape
// every OTHER mode's own call already used before this parameter existed.
//
// ladder (§19.6, Step 43) is the per-repo SetupRerunLadder map
// boot.ComputeSetupRerunLadder computes once per boot, alongside
// workspaceMoved -- consulted ONLY for the exact same cell workspaceMoved
// itself is (repo_image + HookSetup + workspaceMoved: true), to decide
// whether that cell's own rerun can be skipped entirely or handled by a
// cheaper delta script instead of a full setup.sh rerun. nil is a correct,
// safe input (every entry defaults to the conservative "fall through to
// full setup.sh" floor, via ladderFor) -- matching workspaceMoved's own nil
// precedent exactly.
//
// setupRetryDelay (§19.6, Step 43 fix) is the pause runSetupRerunLadder
// waits between the full-setup.sh tier's first failed attempt and its own
// single required retry (see that function's own doc comment) -- consulted
// ONLY inside that one retry path, so any value is a safe input for every
// OTHER call site/outcome.
func RunHooks(
	ctx context.Context,
	sup *supervisor.Supervisor,
	workspaceDir string,
	repos []RepoInfo,
	mode sandboxboot.BootMode,
	workspaceMoved map[string]bool,
	ladder map[string]SetupRerunLadder,
	hookTimeout, stopGrace, setupRetryDelay time.Duration,
) error {
	for _, repo := range repos {
		if err := runRepoHooks(ctx, sup, workspaceDir, repo, mode, workspaceMoved, ladder, hookTimeout, stopGrace, setupRetryDelay); err != nil {
			return err
		}
	}
	return nil
}

// runRepoHooks runs HookSetup then HookStart (in that order) for exactly
// one repo, per §6.4's hook policy (sandboxboot.EvaluateHook) -- the exact
// per-repo body RunHooks used to inline directly in its own loop, factored
// out unchanged (a pure refactor, no behavior change) so
// internal/sandboxagent/boot's new RunBoot (runboot.go) can reuse it for a
// repo that falls back to the hook contract.
//
// Each hook run is timed (recordHookRerunDuration, §19.5(b), now also
// carrying boot_mode/workspace_moved attributes) and its own combined
// stdout+stderr is captured into a bounded, ANSI-stripped tail (runHook's
// own *outputTail, §19.5(a)) -- surfaced in the boot log alongside EITHER
// outcome (fatal or non-fatal): a non-fatal failure is otherwise
// "undiagnosable by construction" (§19.5's own framing, the specific gap
// this closes), and attaching it to a fatal failure too costs nothing
// further and is only ever additional diagnostic information. The §19.4
// setup-rerun decision itself (ShouldRun for HookSetup specifically) is now
// also logged, unconditionally, before that hook is even attempted -- see
// the HookSetup branch below.
func runRepoHooks(
	ctx context.Context,
	sup *supervisor.Supervisor,
	workspaceDir string,
	repo RepoInfo,
	mode sandboxboot.BootMode,
	workspaceMoved map[string]bool,
	ladder map[string]SetupRerunLadder,
	hookTimeout, stopGrace, setupRetryDelay time.Duration,
) error {
	moved := workspaceMovedFor(workspaceMoved, repo.Name)

	for _, hook := range []sandboxboot.Hook{sandboxboot.HookSetup, sandboxboot.HookStart} {
		outcome := sandboxboot.EvaluateHook(mode, hook, repo.Primary, moved)

		if hook == sandboxboot.HookSetup {
			// §19.4's own setup-rerun decision -- previously never logged at
			// all: an operator reading the boot log had no way to tell
			// "working as designed, the repo moved so setup.sh reran" apart
			// from "the build service stopped baking manifests" (both
			// produce an identical-looking log otherwise). Logged uniformly
			// for every mode, not just repo_image -- workspace_moved is only
			// actually CONSULTED by EvaluateHook for the repo_image cell,
			// but including it for every mode costs nothing and lets an
			// operator grep this one line regardless of boot mode.
			platform.Logger(ctx).Info("boot: setup-rerun decision",
				"repo", repo.Name, "boot_mode", string(mode), "workspace_moved", moved,
				"setup_will_run", outcome.ShouldRun, "fatal_on_failure", outcome.FatalOnFailure)
		}

		if !outcome.ShouldRun {
			continue
		}

		// §19.6 (Step 43): the ONE cell this graduated ladder replaces --
		// repo_image's own HookSetup rerun, already known ShouldRun here.
		// Every other (mode, hook) combination -- including repo_image's
		// OWN HookStart, and HookSetup under every OTHER mode -- falls
		// through to the plain runHook call below, completely unchanged
		// from before this Step.
		if hook == sandboxboot.HookSetup && mode == sandboxboot.BootModeRepoImage && moved {
			runSetupRerunLadder(ctx, sup, workspaceDir, repo, ladderFor(ladder, repo.Name), moved, hookTimeout, stopGrace, setupRetryDelay)
			continue
		}

		repoDir := filepath.Join(workspaceDir, repo.Name)
		scriptPath := filepath.Join(repoDir, string(hook))

		present, err := hookScriptPresent(scriptPath)
		if err != nil {
			if outcome.FatalOnFailure {
				return fmt.Errorf("boot: stat %s: %w", scriptPath, err)
			}
			platform.Logger(ctx).Warn("boot: hook stat failed, skipping",
				"repo", repo.Name, "hook", string(hook), "error", err)
			continue
		}
		if !present {
			continue
		}

		start := time.Now()
		tail, runErr := runHook(ctx, sup, scriptPath, repoDir, hookTimeout, stopGrace)
		recordHookRerunDuration(ctx, repo.Name, string(hook), string(mode), moved, time.Since(start).Seconds(), runErr != nil)

		if runErr != nil {
			if outcome.FatalOnFailure {
				// Same "output_tail" structured attribute as the non-fatal
				// path below, logged here (rather than interpolated into
				// the error string with %v) so an operator can grep for
				// output_tail uniformly regardless of which outcome a hook
				// took -- the returned error itself stays a plain %w wrap,
				// its own message never ballooning to a multi-megabyte
				// tail's own size.
				platform.Logger(ctx).Error("boot: hook failed, aborting",
					"repo", repo.Name, "hook", string(hook), "error", runErr,
					"output_tail", tail.Lines())
				return fmt.Errorf("boot: %s in %s failed (fatal): %w", hook, repo.Name, runErr)
			}
			platform.Logger(ctx).Warn("boot: hook failed, continuing",
				"repo", repo.Name, "hook", string(hook), "error", runErr,
				"output_tail", tail.Lines())
		}
	}
	return nil
}

// runSetupRerunLadder implements §19.6's graduated setup-rerun ladder for
// the ONE cell it applies to: BootModeRepoImage's own HookSetup, already
// known ShouldRun (workspaceMoved: true) by the time runRepoHooks calls
// this. Every step is non-fatal by construction (mode is always
// BootModeRepoImage here, whose HookSetup FatalOnFailure is unconditionally
// false -- see sandboxboot.EvaluateHook's own doc comment), so this
// function never returns an error: a setup-rerun-ladder outcome is, by
// definition, always a warn-and-continue outcome, exactly matching the
// plain repo_image path's own behavior before this Step existed.
//
// Ladder, in order (every decision logged individually, §5.3's own
// structured-reason requirement, ladder.RerunReason's own closed
// vocabulary):
//
//  1. DEPENDENCY-DIGEST SKIP (§19.6 first bullet): ladder.DependencySkip ==
//     DependencySkipMatch AND ladder.DeltaEligible -- setup.sh is skipped
//     ENTIRELY, logged reason=skip. Adversarial-review finding B3: a
//     digest match ALONE never licenses this skip. It only proves the
//     dependency-manifest (lockfile) surface is unchanged; it says nothing
//     about setup.sh's own non-package-manager work (§19.4's own "may
//     provision local service stacks, run codegen, seed local state"), so
//     the skip additionally requires ladder.DeltaEligible -- the SAME
//     "setup.sh itself is provably unchanged since the built SHA" fact the
//     delta tier below is built on (SetupRerunLadder.DeltaEligible's own
//     doc comment). Anything else falls through, logged reason=
//     ineligible-fallback -- an unreadable/absent/unrecognized baked
//     digest, a boot-side compute error, a clean proven digest MISMATCH, OR
//     (the B3 addition) a digest match undermined by a changed/unverifiable
//     setup.sh -- all behave identically here (fall through to the delta
//     tier), even though each represents a different underlying reason;
//     that distinction is still preserved for an operator via the
//     "digest_outcome" and "setup_sh_unchanged" attributes logged
//     alongside.
//  2. DELTA SCRIPT (§19.6 third bullet onward): sandboxboot.EvaluateHook's
//     own HookDelta policy row (adversarial-review finding B4: previously
//     dead code -- nothing in production ever called it, so flipping that
//     row had no effect) ShouldRun, AND ladder.DeltaEligible -- sync.sh
//     runs INSTEAD of setup.sh, sharing hookTimeout (never a new timeout
//     constant). A repo with no sync.sh on disk falls through silently
//     (reason=ineligible-fallback), exactly like any other absent hook
//     always has. A delta-script FAILURE (non-fatal) also falls through to
//     full setup.sh -- the failure ladder's own "delta fails -> warn -> run
//     full setup.sh" step -- logged reason=delta regardless (choosing to
//     attempt sync.sh IS this tier's own decision; whether that attempt
//     then succeeded is a separate, already-logged runtime outcome, not a
//     second ladder decision).
//  3. FULL setup.sh: today's unconditional repo_image rerun, largely
//     unchanged -- the ladder's own floor, reached whenever neither tier
//     above actually resolved the decision. Logged reason=full. §19.6 fix
//     (the manifest-digest bullet: "retry the install on transient
//     failure, then warn -- never fail the boot on it"): a FAILED first
//     attempt here is retried EXACTLY ONCE, after a pause of
//     setupRetryDelay (never an unbounded backoff loop -- §19.6 asks for a
//     retry, not a resilience framework), logged as its own decision
//     (reason=retry) before the second attempt runs. The retry itself
//     never changes severity: a second failure still warns and continues,
//     identical to today's single-attempt outcome -- this tier's own
//     unconditional non-fatal-by-construction guarantee (this function's
//     own top doc comment) covers both attempts equally.
//
// moved is workspaceMoved for this exact repo -- always true at the one
// production call site (runRepoHooks only enters this function inside its
// own `mode == BootModeRepoImage && moved` branch), threaded through
// explicitly (rather than hardcoded) so the HookDelta EvaluateHook
// consultation below uses the real value, and so a white-box test can
// exercise the B4 wiring directly (depsladder_internal_test.go).
//
// setupRetryDelay is the pause between the full-setup.sh tier's first
// failed attempt and its own single retry (item 3 above) -- irrelevant to
// every other path through this function (the digest skip, the delta
// tier, and a full-setup.sh attempt that succeeds on its first try never
// consult it at all).
func runSetupRerunLadder(ctx context.Context, sup *supervisor.Supervisor, workspaceDir string, repo RepoInfo, ladder SetupRerunLadder, moved bool, hookTimeout, stopGrace, setupRetryDelay time.Duration) {
	logger := platform.Logger(ctx)
	repoDir := filepath.Join(workspaceDir, repo.Name)

	// B3: a digest match can only skip setup.sh entirely when setup.sh
	// itself is ALSO provably unchanged -- see SetupRerunLadder.DeltaEligible's
	// own doc comment for why this is the identical fact the delta tier
	// below consults, not a new check.
	if ladder.DependencySkip == DependencySkipMatch && ladder.DeltaEligible {
		logger.Info("boot: setup-rerun ladder decision",
			"repo", repo.Name, "tier", "digest", "outcome", string(RerunReasonSkip),
			"digest_outcome", string(ladder.DependencySkip))
		return
	}
	if ladder.DependencySkip == DependencySkipMatch {
		logger.Info("boot: setup-rerun ladder decision",
			"repo", repo.Name, "tier", "digest", "outcome", string(RerunReasonIneligibleFallback),
			"digest_outcome", string(ladder.DependencySkip), "setup_sh_unchanged", ladder.DeltaEligible)
	} else {
		logger.Info("boot: setup-rerun ladder decision",
			"repo", repo.Name, "tier", "digest", "outcome", string(RerunReasonIneligibleFallback),
			"digest_outcome", string(ladder.DependencySkip))
	}

	// B4: the delta tier's own eligibility now ALSO routes through
	// sandboxboot.EvaluateHook's HookDelta policy row -- the canonical
	// policy table, not a second, hand-duplicated envelope check. In every
	// real boot this evaluates to the same envelope runRepoHooks already
	// gated entry to this function on (mode == BootModeRepoImage &&
	// moved), so this changes no observable behavior today; it makes that
	// policy row the actual authority for sync.sh, matching HookSetup and
	// HookStart, so a future edit to the row is no longer silently ignored.
	deltaPolicy := sandboxboot.EvaluateHook(sandboxboot.BootModeRepoImage, sandboxboot.HookDelta, repo.Primary, moved)
	if deltaPolicy.ShouldRun && ladder.DeltaEligible {
		ran, ok := runNamedHookNonFatal(ctx, sup, repoDir, repo.Name, string(sandboxboot.HookDelta), sandboxboot.BootModeRepoImage, moved, hookTimeout, stopGrace)
		if ran {
			logger.Info("boot: setup-rerun ladder decision",
				"repo", repo.Name, "tier", "delta", "outcome", string(RerunReasonDelta), "succeeded", ok)
			if ok {
				return
			}
			// Delta script failed -- fall through to full setup.sh, the
			// failure ladder's own explicit "delta fails -> warn -> run
			// full setup.sh" step. runNamedHookNonFatal already logged its
			// own Warn with the failure's output_tail.
		}
		// ran == false: no sync.sh present on disk (or its own stat
		// failed) -- a routine, silent fall-through, matching
		// hookScriptPresent's own existing "absent script is normal, not
		// even a warning" precedent everywhere else in this package.
	} else {
		logger.Info("boot: setup-rerun ladder decision",
			"repo", repo.Name, "tier", "delta", "outcome", string(RerunReasonIneligibleFallback))
	}

	logger.Info("boot: setup-rerun ladder decision",
		"repo", repo.Name, "tier", "full", "outcome", string(RerunReasonFull))
	ran, ok := runNamedHookNonFatal(ctx, sup, repoDir, repo.Name, string(sandboxboot.HookSetup), sandboxboot.BootModeRepoImage, moved, hookTimeout, stopGrace)
	if !ran || ok {
		// ran == false: no setup.sh on disk at all (routine, silent,
		// nothing to retry). ok == true: the first attempt already
		// succeeded -- no retry needed, and none of §19.6's own retry
		// language ("on transient failure") applies to a run that never
		// failed.
		return
	}

	// §19.6 fix (the manifest-digest bullet): "retry the install on
	// transient failure, then warn -- never fail the boot on it". The
	// FIRST attempt's own failure was already logged by
	// runNamedHookNonFatal above (its own "boot: hook failed, continuing"
	// Warn, carrying output_tail per §19.5(a)) -- this line records the
	// RETRY decision itself, before the second attempt runs, so an
	// operator can tell "gave up after one try" apart from "retried once,
	// per §19.6" purely from the boot log.
	logger.Info("boot: setup-rerun ladder decision",
		"repo", repo.Name, "tier", "full", "outcome", string(RerunReasonRetry))

	if !waitSetupRetryDelay(ctx, setupRetryDelay) {
		// ctx is already done -- boot is being torn down. The first
		// attempt's own failure is already logged above; attempting a
		// second spawn against an already-cancelled/expired context would
		// not be a meaningfully different attempt, so this simply stops
		// here, exactly matching today's single-attempt warn-and-continue
		// outcome.
		return
	}

	// Exactly ONE retry -- not a loop: the return value is intentionally
	// discarded here (never a THIRD attempt regardless of outcome).
	// Success or failure, this second attempt's own outcome is already
	// fully logged by runNamedHookNonFatal itself (nothing on success; a
	// "boot: hook failed, continuing" Warn with output_tail on failure) --
	// a second failure therefore still warns and continues, identical in
	// severity to today's single-attempt behavior, never escalated to
	// fatal.
	runNamedHookNonFatal(ctx, sup, repoDir, repo.Name, string(sandboxboot.HookSetup), sandboxboot.BootModeRepoImage, moved, hookTimeout, stopGrace)
}

// waitSetupRetryDelay waits d, honoring ctx cancellation -- mirrors
// internal/adapters/outbound/opencode's own waitTransientRetryBackoff
// precedent exactly (time.NewTimer + deferred Stop, not time.After, so the
// timer is released immediately rather than lingering until it would have
// fired): the identical shape for the identical need, a single bounded
// pause before one retry of an operation that just failed. Returns true if
// d elapsed normally, false if ctx was done first -- a bool return (rather
// than mirroring that precedent's own error return) is all this caller
// needs, since runSetupRerunLadder never returns an error itself (its own
// top doc comment: always non-fatal by construction).
func waitSetupRetryDelay(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// runNamedHookNonFatal runs the script at repoDir/scriptName if present,
// mirroring runRepoHooks' own inline stat -> spawn -> time -> record -> log
// sequence for a NON-FATAL hook exactly -- factored out specifically for
// runSetupRerunLadder's own two script attempts (sync.sh, then a
// fallback-or-primary full setup.sh), both of which are always non-fatal
// by construction (mode is always BootModeRepoImage at both call sites).
// Never returns an error for that reason: every caller already knows this
// attempt cannot fail the boot.
//
// Returns (ran, ok): ran is false when the script was absent (or its own
// stat failed) -- nothing was attempted, ok is meaningless. ran is true
// once the script was actually spawned; ok then reports whether it
// succeeded. recordHookRerunDuration is only ever called when ran is true,
// exactly mirroring runRepoHooks' own existing "absent hook records
// nothing" behavior.
func runNamedHookNonFatal(ctx context.Context, sup *supervisor.Supervisor, repoDir, repoName, scriptName string, mode sandboxboot.BootMode, moved bool, hookTimeout, stopGrace time.Duration) (ran, ok bool) {
	scriptPath := filepath.Join(repoDir, scriptName)

	present, statErr := hookScriptPresent(scriptPath)
	if statErr != nil {
		platform.Logger(ctx).Warn("boot: hook stat failed, skipping",
			"repo", repoName, "hook", scriptName, "error", statErr)
		return false, false
	}
	if !present {
		return false, false
	}

	start := time.Now()
	tail, runErr := runHook(ctx, sup, scriptPath, repoDir, hookTimeout, stopGrace)
	recordHookRerunDuration(ctx, repoName, scriptName, string(mode), moved, time.Since(start).Seconds(), runErr != nil)

	if runErr != nil {
		platform.Logger(ctx).Warn("boot: hook failed, continuing",
			"repo", repoName, "hook", scriptName, "error", runErr, "output_tail", tail.Lines())
		return true, false
	}
	return true, true
}

// hookScriptPresent reports whether scriptPath exists on disk. A genuine
// stat failure other than "does not exist" (e.g. permission denied) is
// returned as an error rather than silently treated the same as absence.
func hookScriptPresent(scriptPath string) (bool, error) {
	_, err := os.Stat(scriptPath)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// runHook spawns scriptPath under sup and waits for it, bounded by
// hookTimeout. If it is still running when hookTimeout expires (or ctx
// itself is done first), it is stopped (bounded by stopGrace using the
// outer ctx, not the already-expired hook-scoped one, so the SIGTERM ->
// SIGKILL grace period is honored rather than short-circuited) and the
// timeout/cancellation is reported as the failure. If it's not executable,
// Spawn's own error surfaces here directly -- a real error, never silently
// swallowed.
//
// Always returns a non-nil *outputTail, even on a spawn failure (an empty
// one in that case -- the process never ran, so there is nothing to
// capture) -- so every caller can unconditionally call tail.Lines()
// without a nil check (§19.5(a)): a caller-held, bounded, ANSI-stripped
// tail of the hook's own combined stdout+stderr, wired through
// supervisor.Spec's existing Stdout/Stderr seam (mirroring gitclone's own
// applySparseCheckout precedent for that exact seam) and held ENTIRELY by
// this function's own local variable, independent of the bounded
// proc.Wait call below -- a timeout-triggered proc.Stop can therefore
// never lose a buffer that was never inside the cancelled operation to
// begin with.
func runHook(ctx context.Context, sup *supervisor.Supervisor, scriptPath, dir string, hookTimeout, stopGrace time.Duration) (*outputTail, error) {
	tail := newOutputTail()

	proc, err := sup.Spawn(supervisor.Spec{
		Path: scriptPath,
		Dir:  dir,
		// A repo's own setup.sh/start.sh comes from the SESSION'S OWN
		// REPO -- it has no legitimate need to see the sandbox's own
		// plaintext bearer token (NARVI_SESSION_CONFIG) either, so it is
		// excluded here exactly like opencodeproc.Spawn's own call.
		Env: supervisor.EnvWithout(SessionConfigEnvVar),
		// tail captures BOTH streams into one combined, bounded,
		// ANSI-stripped tail (§19.5(a)) -- a single io.Writer used for both
		// fields is safe: outputTail.Write is mutex-guarded against
		// concurrent calls, matching exec.Cmd's own documented behavior for
		// sharing one Writer across Stdout/Stderr.
		Stdout: tail,
		Stderr: tail,
	})
	if err != nil {
		return tail, fmt.Errorf("spawn %s: %w", scriptPath, err)
	}

	hookCtx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()

	result, waitErr := proc.Wait(hookCtx)
	if waitErr != nil {
		_ = proc.Stop(ctx, stopGrace)
		return tail, fmt.Errorf("%s: did not complete within %s: %w", scriptPath, hookTimeout, waitErr)
	}

	if result.Err != nil {
		return tail, fmt.Errorf("%s: %w", scriptPath, result.Err)
	}
	if result.ExitCode != 0 {
		return tail, fmt.Errorf("%s: exited %d", scriptPath, result.ExitCode)
	}
	return tail, nil
}
