package boot

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/khazaddev/narvi/internal/domain/sandboxboot"
	"github.com/khazaddev/narvi/internal/sandboxagent/services"
	"github.com/khazaddev/narvi/internal/sandboxagent/supervisor"
)

// RunBoot is the top-level per-repo boot dispatcher (§14.2): for each repo,
// in order, if <workspaceDir>/<repo.Name>/.narvi/services.yml is present,
// its declared services are supervised (internal/sandboxagent/services);
// otherwise this package's own per-repo hook logic (runRepoHooks, §6.4,
// Step 13) runs unchanged -- backward compatible, no forced migration
// (§14.2: "if services.yml is absent, sandbox-agent falls back to the
// current setup.sh/start.sh contract unchanged").
//
// On ANY fatal failure (from either path), RunBoot stops processing
// further repos and returns immediately -- exactly like RunHooks already
// does on its own, so a later repo is never even attempted once an earlier
// one has fatally failed.
//
// A genuine services.Locate error (a real stat failure, not "absent") is
// propagated as a real error, never silently treated as "fall back to
// hooks" -- mirroring hookScriptPresent's own stat-error distinction. A
// manifest that IS present but fails services.Load's own YAML-decode or
// servicemanifest.Validate validation is likewise a real, propagated error:
// a malformed services.yml is an authoring bug worth surfacing loudly, not
// masking behind a silent fallback to the hook contract.
//
// workspaceMoved (§19.4) is passed straight through to
// runRepoHooks for the services.yml-absent (hook-contract) branch -- see
// RunHooks's own doc comment. A repo supervised via services.yml never
// consults it at all: that path never runs setup.sh/start.sh through
// sandboxboot.EvaluateHook in the first place (§14.2's own "no new
// supervision code path" design), so workspaceMoved has nothing to gate
// there.
//
// ladder (§19.6) is passed straight through to runRepoHooks
// alongside workspaceMoved, for the identical reason and the identical
// services.yml-branch exemption.
//
// setupRetryDelay (§19.6 fix) is likewise passed straight through
// to runRepoHooks -- consulted only inside its own full-setup.sh retry
// path (runSetupRerunLadder's own doc comment), so it is a safe input
// regardless of which branch (services.yml or hook-contract) a given repo
// actually takes.
//
// secretEnv (§27.1, adversarial-review HIGH fix) is zero or more
// already-built "NAME=VALUE" entries -- a session's own resolved general
// sandbox_secrets rows -- passed straight through to runRepoHooks for the
// hook-contract branch, and appended to services.Run's own env for the
// services.yml branch (both AFTER supervisor.EnvWithout(SessionConfigEnvVar),
// exactly mirroring that call's own existing append shape) -- §27.1's own
// two remaining named spawn targets besides opencode serve (threaded at
// its own call site, cmd/sandbox-agent/main.go, since RunBoot has no
// opencode-serve call of its own). See runHook's/RunHooks' own doc
// comments for why this is threaded rather than os.Setenv onto
// sandbox-agent's own process.
func RunBoot(
	ctx context.Context,
	sup *supervisor.Supervisor,
	workspaceDir string,
	repos []RepoInfo,
	mode sandboxboot.BootMode,
	workspaceMoved map[string]bool,
	ladder map[string]SetupRerunLadder,
	secretEnv []string,
	reporter services.ProgressReporter,
	hookTimeout, stopGrace, readinessTimeout, readinessPollInterval, setupRetryDelay time.Duration,
) error {
	for _, repo := range repos {
		repoDir := filepath.Join(workspaceDir, repo.Name)

		manifestPath, found, err := services.Locate(repoDir)
		if err != nil {
			return fmt.Errorf("boot: locate services.yml for %s: %w", repo.Name, err)
		}

		if !found {
			if err := runRepoHooks(ctx, sup, workspaceDir, repo, mode, workspaceMoved, ladder, secretEnv, hookTimeout, stopGrace, setupRetryDelay); err != nil {
				return err
			}
			continue
		}

		manifest, err := services.Load(manifestPath)
		if err != nil {
			return fmt.Errorf("boot: load services.yml for %s: %w", repo.Name, err)
		}

		// supervisor.EnvWithout(SessionConfigEnvVar): a repo's own
		// services.yml command has no more legitimate need to see the
		// sandbox's own plaintext bearer token than its setup.sh/start.sh
		// sibling does (runRepoHooks' own runHook call, same reasoning) --
		// see services.Run's own doc comment for why this package computes
		// the exclusion itself rather than services.Run importing this
		// package back (which would create an import cycle, since this
		// package already imports services). secretEnv is appended on top
		// of that filtered base -- §27.1's own explicit "services.yml
		// services" spawn target.
		if err := services.Run(ctx, sup, repoDir, manifest, append(supervisor.EnvWithout(SessionConfigEnvVar), secretEnv...), reporter, readinessTimeout, readinessPollInterval); err != nil {
			return fmt.Errorf("boot: services.yml supervision for %s failed: %w", repo.Name, err)
		}
	}
	return nil
}
