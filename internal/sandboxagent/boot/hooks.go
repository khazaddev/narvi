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
func RunHooks(
	ctx context.Context,
	sup *supervisor.Supervisor,
	workspaceDir string,
	repos []RepoInfo,
	mode sandboxboot.BootMode,
	hookTimeout, stopGrace time.Duration,
) error {
	for _, repo := range repos {
		if err := runRepoHooks(ctx, sup, workspaceDir, repo, mode, hookTimeout, stopGrace); err != nil {
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
func runRepoHooks(
	ctx context.Context,
	sup *supervisor.Supervisor,
	workspaceDir string,
	repo RepoInfo,
	mode sandboxboot.BootMode,
	hookTimeout, stopGrace time.Duration,
) error {
	for _, hook := range []sandboxboot.Hook{sandboxboot.HookSetup, sandboxboot.HookStart} {
		outcome := sandboxboot.EvaluateHook(mode, hook, repo.Primary)
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

		if runErr := runHook(ctx, sup, scriptPath, repoDir, hookTimeout, stopGrace); runErr != nil {
			if outcome.FatalOnFailure {
				return fmt.Errorf("boot: %s in %s failed (fatal): %w", hook, repo.Name, runErr)
			}
			platform.Logger(ctx).Warn("boot: hook failed, continuing",
				"repo", repo.Name, "hook", string(hook), "error", runErr)
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
func runHook(ctx context.Context, sup *supervisor.Supervisor, scriptPath, dir string, hookTimeout, stopGrace time.Duration) error {
	proc, err := sup.Spawn(supervisor.Spec{
		Path: scriptPath,
		Dir:  dir,
		// A repo's own setup.sh/start.sh comes from the SESSION'S OWN
		// REPO -- it has no legitimate need to see the sandbox's own
		// plaintext bearer token (NARVI_SESSION_CONFIG) either, so it is
		// excluded here exactly like opencodeproc.Spawn's own call.
		Env: supervisor.EnvWithout(SessionConfigEnvVar),
	})
	if err != nil {
		return fmt.Errorf("spawn %s: %w", scriptPath, err)
	}

	hookCtx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()

	result, waitErr := proc.Wait(hookCtx)
	if waitErr != nil {
		_ = proc.Stop(ctx, stopGrace)
		return fmt.Errorf("%s: did not complete within %s: %w", scriptPath, hookTimeout, waitErr)
	}

	if result.Err != nil {
		return fmt.Errorf("%s: %w", scriptPath, result.Err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("%s: exited %d", scriptPath, result.ExitCode)
	}
	return nil
}
