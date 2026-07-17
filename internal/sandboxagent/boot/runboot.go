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
func RunBoot(
	ctx context.Context,
	sup *supervisor.Supervisor,
	workspaceDir string,
	repos []RepoInfo,
	mode sandboxboot.BootMode,
	reporter services.ProgressReporter,
	hookTimeout, stopGrace, readinessTimeout, readinessPollInterval time.Duration,
) error {
	for _, repo := range repos {
		repoDir := filepath.Join(workspaceDir, repo.Name)

		manifestPath, found, err := services.Locate(repoDir)
		if err != nil {
			return fmt.Errorf("boot: locate services.yml for %s: %w", repo.Name, err)
		}

		if !found {
			if err := runRepoHooks(ctx, sup, workspaceDir, repo, mode, hookTimeout, stopGrace); err != nil {
				return err
			}
			continue
		}

		manifest, err := services.Load(manifestPath)
		if err != nil {
			return fmt.Errorf("boot: load services.yml for %s: %w", repo.Name, err)
		}

		if err := services.Run(ctx, sup, repoDir, manifest, reporter, readinessTimeout, readinessPollInterval); err != nil {
			return fmt.Errorf("boot: services.yml supervision for %s failed: %w", repo.Name, err)
		}
	}
	return nil
}
