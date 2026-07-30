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
func RunHooks(
	ctx context.Context,
	sup *supervisor.Supervisor,
	workspaceDir string,
	repos []RepoInfo,
	mode sandboxboot.BootMode,
	workspaceMoved map[string]bool,
	hookTimeout, stopGrace time.Duration,
) error {
	for _, repo := range repos {
		if err := runRepoHooks(ctx, sup, workspaceDir, repo, mode, workspaceMoved, hookTimeout, stopGrace); err != nil {
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
// Each hook run is timed (recordHookRerunDuration, §19.5(b)) and its own
// combined stdout+stderr is captured into a bounded, ANSI-stripped tail
// (runHook's own *outputTail, §19.5(a)) -- surfaced in the boot log
// alongside EITHER outcome (fatal or non-fatal): a non-fatal failure is
// otherwise "undiagnosable by construction" (§19.5's own framing, the
// specific gap this closes), and attaching it to a fatal failure too costs
// nothing further and is only ever additional diagnostic information.
func runRepoHooks(
	ctx context.Context,
	sup *supervisor.Supervisor,
	workspaceDir string,
	repo RepoInfo,
	mode sandboxboot.BootMode,
	workspaceMoved map[string]bool,
	hookTimeout, stopGrace time.Duration,
) error {
	moved := workspaceMovedFor(workspaceMoved, repo.Name)

	for _, hook := range []sandboxboot.Hook{sandboxboot.HookSetup, sandboxboot.HookStart} {
		outcome := sandboxboot.EvaluateHook(mode, hook, repo.Primary, moved)
		if !outcome.ShouldRun {
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
		recordHookRerunDuration(ctx, repo.Name, string(hook), time.Since(start).Seconds(), runErr != nil)

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
