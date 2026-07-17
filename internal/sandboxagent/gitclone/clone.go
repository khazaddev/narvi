package gitclone

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/internal/sandboxagent/supervisor"
)

// CloneResult is one repo's outcome from CloneAll.
type CloneResult struct {
	Repo sessionconfig.SessionConfigReposElem
	// Primary is true for exactly the repo at position 0 in the manifest
	// (§3.4: "position 0 = primary").
	Primary bool
	// Dir is workspaceDir/<Repo.Name> -- always set, even when Err is
	// non-nil, so callers (WriteAgentsManifest, boot logging) always know
	// where this repo was supposed to land.
	Dir string
	// Err is non-nil if this repo's clone failed.
	Err error
}

// CloneAll clones every repo in repos, IN ORDER, into workspaceDir/<name>,
// via a real `git clone` subprocess spawned through sup (never a bare
// exec.Command) -- see doc.go for the full criticality/credential-helper/
// branch semantics. workspaceDir is created (MkdirAll) if it does not
// already exist, since `git clone` itself requires its target's PARENT
// directory to already exist.
//
// On a primary (repos[0]) clone failure, CloneAll returns immediately with
// a fatal error -- no repo after it is attempted, matching RunHooks' own
// "any fatal failure stops immediately" semantics exactly. A secondary
// repo's clone failure is logged as a warning and does not stop the loop;
// subsequent repos still get cloned. results always reflects every repo
// actually attempted (in order), regardless of outcome.
func CloneAll(
	ctx context.Context,
	sup *supervisor.Supervisor,
	workspaceDir string,
	repos []sessionconfig.SessionConfigReposElem,
	cloneTimeout, stopGrace time.Duration,
) ([]CloneResult, error) {
	if len(repos) == 0 {
		return nil, nil
	}

	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		return nil, fmt.Errorf("gitclone: create workspace dir %s: %w", workspaceDir, err)
	}

	credHelperArg, err := credHelperGitArg()
	if err != nil {
		return nil, fmt.Errorf("gitclone: determine credential helper: %w", err)
	}

	results := make([]CloneResult, 0, len(repos))
	for i, repo := range repos {
		primary := i == 0
		dir := filepath.Join(workspaceDir, repo.Name)

		cloneErr := cloneOne(ctx, sup, credHelperArg, repo, dir, cloneTimeout, stopGrace)
		results = append(results, CloneResult{Repo: repo, Primary: primary, Dir: dir, Err: cloneErr})

		if cloneErr == nil {
			continue
		}

		if primary {
			return results, fmt.Errorf("gitclone: primary repo %q failed to clone (fatal): %w", repo.Name, cloneErr)
		}
		platform.Logger(ctx).Warn("gitclone: secondary repo failed to clone, continuing",
			"repo", repo.Name, "error", cloneErr)
	}

	return results, nil
}

// cloneOne spawns `git clone` for exactly one repo and waits for it,
// bounded by cloneTimeout -- mirroring internal/sandboxagent/boot's own
// runHook precisely: a hang is stopped (bounded by stopGrace, using the
// OUTER ctx for the Stop call, not the already-expired clone-scoped
// context) and reported as a timeout failure; a non-zero exit or a wait
// failure is likewise a real, returned error.
func cloneOne(
	ctx context.Context,
	sup *supervisor.Supervisor,
	credHelperArg string,
	repo sessionconfig.SessionConfigReposElem,
	dir string,
	cloneTimeout, stopGrace time.Duration,
) error {
	args := []string{"clone", "-c", "credential.helper=" + credHelperArg}
	if repo.Branch != nil {
		args = append(args, "--branch", *repo.Branch)
	}
	args = append(args, repo.Url, dir)

	proc, err := sup.Spawn(supervisor.Spec{
		Path: "git",
		Args: args,
	})
	if err != nil {
		return fmt.Errorf("spawn git clone for %s: %w", repo.Name, err)
	}

	cloneCtx, cancel := context.WithTimeout(ctx, cloneTimeout)
	defer cancel()

	result, waitErr := proc.Wait(cloneCtx)
	if waitErr != nil {
		_ = proc.Stop(ctx, stopGrace)
		return fmt.Errorf("git clone %s: did not complete within %s: %w", repo.Name, cloneTimeout, waitErr)
	}

	if result.Err != nil {
		return fmt.Errorf("git clone %s: %w", repo.Name, result.Err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("git clone %s: exited %d", repo.Name, result.ExitCode)
	}
	return nil
}

// credHelperGitArg builds the exact `-c credential.helper=...` value: the
// CURRENTLY RUNNING binary's own absolute path (os.Executable()) plus the
// "credential-helper" subcommand, prefixed with `!` so git runs this exact
// shell command rather than treating it as a suffix appended to
// "git-credential-" (git itself appends the final "get"/"store"/"erase"
// argument when it invokes the helper).
func credHelperGitArg() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("os.Executable: %w", err)
	}
	return fmt.Sprintf("!'%s' credential-helper", exePath), nil
}
