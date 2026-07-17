package boot

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/khazaddev/narvi/internal/domain/sandboxboot"
)

// CollectFingerprint assembles the boot fingerprint §5.3 requires
// sandbox-agent log first -- before any other line -- directly from cfg,
// plus a best-effort repo-SHA discovery pass over cfg.WorkspaceDir.
// repoSHATimeout bounds each individual repo's `git rev-parse` call (see
// DiscoverRepoSHAs); callers pass platform.Timeouts.RepoSHADiscoveryTimeout
// (never a literal -- this package must not import time.Duration unit
// literals per §5.4/§11, enforced by tools/lint/narvichecks/notimeliteral).
func CollectFingerprint(cfg Config, repoSHATimeout time.Duration) sandboxboot.BootFingerprint {
	return sandboxboot.BootFingerprint{
		AgentVersion: cfg.AgentVersion,
		ImageDigest:  cfg.ImageDigest,
		BootMode:     cfg.BootMode,
		RepoSHAs:     DiscoverRepoSHAs(cfg.WorkspaceDir, repoSHATimeout),
	}
}

// DiscoverRepoSHAs globs the immediate subdirectories of workspaceDir; for
// each that contains a .git entry, shells out to `git -C <dir> rev-parse
// HEAD` (git is always present in sandbox images per §6.4's own git-based
// workflow), each call bounded by timeout. Never returns an error: any
// single repo's SHA that can't be determined (workspaceDir itself missing,
// not a git repo, git binary missing, command failed, timed out) is simply
// omitted from the returned map. This function does no logging of its own
// -- it stays a pure-ish, easily-testable function that returns data; the
// CALLER decides whether/how to log an omission (at most debug-level, per
// this Step's own instructions).
func DiscoverRepoSHAs(workspaceDir string, timeout time.Duration) map[string]string {
	shas := make(map[string]string)

	entries, err := os.ReadDir(workspaceDir)
	if err != nil {
		return shas
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		repoDir := filepath.Join(workspaceDir, entry.Name())
		if _, statErr := os.Stat(filepath.Join(repoDir, ".git")); statErr != nil {
			continue
		}

		sha, ok := repoHeadSHA(repoDir, timeout)
		if !ok {
			continue
		}
		shas[entry.Name()] = sha
	}

	return shas
}

// repoHeadSHA runs `git -C repoDir rev-parse HEAD` bounded by timeout,
// returning (sha, true) on success or ("", false) on any failure
// whatsoever (non-git directory, git missing, non-zero exit, timeout).
func repoHeadSHA(repoDir string, timeout time.Duration) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}

	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return "", false
	}
	return sha, true
}
