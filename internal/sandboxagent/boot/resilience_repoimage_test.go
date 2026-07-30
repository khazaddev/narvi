package boot_test

// Two new §9.3-class resilience scenarios (Step 42, §19.4/§19.9's own
// "stale-image boot" and "non-idempotent-setup boot" additions), exercised
// through boot.RunBoot itself -- the same real orchestration point
// cmd/sandbox-agent's own runBootSequence calls (hook policy +
// workspaceMoved + output-tail capture + telemetry, all wired together
// against a REAL git repo and a REAL /narvi/image-manifest.json-shaped
// manifest), not a bare EvaluateHook table-test case. This is where the
// actual logic under test lives (internal/sandboxagent/boot), and
// cmd/sandbox-agent's own thin main.go wiring around LoadImageManifest/
// ComputeWorkspaceMoved is already covered by that package's own existing
// bootsequence_cleanbuild_integration_test.go precedent -- these two
// scenarios do not need a second, duplicate proof through main.go's own
// os.Executable()-sensitive subprocess machinery.

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/khazaddev/narvi/internal/domain/sandboxboot"
	"github.com/khazaddev/narvi/internal/sandboxagent/boot"
	"github.com/khazaddev/narvi/internal/sandboxagent/supervisor"
)

// TestResilienceScenario_StaleImageBoot_WorkspaceMovedFiresSetupReruns
// proves §19.4's own headline contract end to end: a repo_image boot whose
// baked manifest names a DIFFERENT built SHA than the repo's own real,
// checked-out HEAD -- workspaceMoved fires, and setup.sh reruns,
// non-fatally, through the real hook-policy/output-capture machinery
// RunBoot orchestrates.
func TestResilienceScenario_StaleImageBoot_WorkspaceMovedFiresSetupReruns(t *testing.T) {
	workspaceDir := t.TempDir()
	repoDir := filepath.Join(workspaceDir, "repo1")
	mkdirAll(t, repoDir)
	initGitRepo(t, repoDir) // fingerprint_test.go's own helper: one real commit

	rerunMarker := filepath.Join(workspaceDir, "setup-rerun-marker")
	writeScript(t, filepath.Join(repoDir, "setup.sh"), "touch "+rerunMarker)

	currentSHA := gitRevParseHEAD(t, repoDir)

	manifest := boot.ImageManifest{
		Fingerprint:   "fp-stale-image",
		BuiltRepoShas: map[string]string{"repo1": "sha-different-from-current-" + currentSHA[:8]},
	}
	workspaceMoved := boot.ComputeWorkspaceMoved(manifest, true, map[string]string{"repo1": currentSHA})
	if !workspaceMoved["repo1"] {
		t.Fatalf("precondition failed: ComputeWorkspaceMoved()[repo1] = false, want true (manifest SHA deliberately differs from current)")
	}

	sup := supervisor.New()
	repos := []boot.RepoInfo{{Name: "repo1", Primary: true}}

	err := boot.RunBoot(context.Background(), sup, workspaceDir, repos, sandboxboot.BootModeRepoImage, workspaceMoved,
		noopReporter, 10*time.Second, time.Second, testReadinessTimeout, testReadinessPollInterval)
	if err != nil {
		t.Fatalf("RunBoot() error = %v, want nil (a moved workspace's setup.sh rerun must be non-fatal)", err)
	}

	assertFileExists(t, rerunMarker)
}

// TestResilienceScenario_StaleImageBoot_WorkspaceUnmoved_SetupSkipped proves
// the companion "zero regression" half: an UNMOVED workspace (manifest SHA
// matches current HEAD exactly) still skips setup.sh entirely under
// repo_image, exactly as before §19.4's amendment.
func TestResilienceScenario_StaleImageBoot_WorkspaceUnmoved_SetupSkipped(t *testing.T) {
	workspaceDir := t.TempDir()
	repoDir := filepath.Join(workspaceDir, "repo1")
	mkdirAll(t, repoDir)
	initGitRepo(t, repoDir)

	rerunMarker := filepath.Join(workspaceDir, "setup-should-not-run-marker")
	writeScript(t, filepath.Join(repoDir, "setup.sh"), "touch "+rerunMarker)

	currentSHA := gitRevParseHEAD(t, repoDir)

	manifest := boot.ImageManifest{
		Fingerprint:   "fp-unmoved-image",
		BuiltRepoShas: map[string]string{"repo1": currentSHA}, // matches exactly
	}
	workspaceMoved := boot.ComputeWorkspaceMoved(manifest, true, map[string]string{"repo1": currentSHA})
	if workspaceMoved["repo1"] {
		t.Fatalf("precondition failed: ComputeWorkspaceMoved()[repo1] = true, want false (manifest SHA deliberately matches current)")
	}

	sup := supervisor.New()
	repos := []boot.RepoInfo{{Name: "repo1", Primary: true}}

	err := boot.RunBoot(context.Background(), sup, workspaceDir, repos, sandboxboot.BootModeRepoImage, workspaceMoved,
		noopReporter, 10*time.Second, time.Second, testReadinessTimeout, testReadinessPollInterval)
	if err != nil {
		t.Fatalf("RunBoot() error = %v, want nil", err)
	}

	assertFileAbsent(t, rerunMarker)
}

// TestResilienceScenario_NonIdempotentSetupBoot_NonFatalFailure_VisibleInOutputTail
// proves §19.4's own non-fatal-severity guarantee for the OTHER real-world
// failure mode: a setup.sh that is NOT safely re-runnable (fails on a
// rerun against an already-warm workspace) must still let the boot
// succeed overall -- with the failure's own diagnostic output actually
// captured and surfaced (§19.5(a)), not silently swallowed the way a
// non-fatal hook failure used to be before this Step ("undiagnosable by
// construction").
func TestResilienceScenario_NonIdempotentSetupBoot_NonFatalFailure_VisibleInOutputTail(t *testing.T) {
	// Not t.Parallel(): swaps slog's global default logger, mirroring
	// hooks_output_test.go's own identical precedent.
	handler := &recordingHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	workspaceDir := t.TempDir()
	repoDir := filepath.Join(workspaceDir, "repo1")
	mkdirAll(t, repoDir)
	initGitRepo(t, repoDir)

	writeScript(t, filepath.Join(repoDir, "setup.sh"),
		"echo 'installing dependency that is broken on rerun' >&2\nexit 1")

	currentSHA := gitRevParseHEAD(t, repoDir)
	manifest := boot.ImageManifest{
		Fingerprint:   "fp-non-idempotent-setup",
		BuiltRepoShas: map[string]string{"repo1": "sha-different-from-current-" + currentSHA[:8]},
	}
	workspaceMoved := boot.ComputeWorkspaceMoved(manifest, true, map[string]string{"repo1": currentSHA})

	sup := supervisor.New()
	repos := []boot.RepoInfo{{Name: "repo1", Primary: true}}

	err := boot.RunBoot(context.Background(), sup, workspaceDir, repos, sandboxboot.BootModeRepoImage, workspaceMoved,
		noopReporter, 10*time.Second, time.Second, testReadinessTimeout, testReadinessPollInterval)
	if err != nil {
		t.Fatalf("RunBoot() error = %v, want nil (a non-idempotent setup.sh's rerun failure must be non-fatal -- boot still succeeds)", err)
	}

	rawTail, ok := handler.lastAttr("output_tail")
	if !ok {
		t.Fatal("no Warn log line carried an output_tail attribute -- the failure is undiagnosable, exactly the gap §19.5(a) exists to close")
	}
	lines, ok := rawTail.Any().([]string)
	if !ok {
		t.Fatalf("output_tail attribute = %T, want []string", rawTail.Any())
	}
	found := false
	for _, line := range lines {
		if line == "installing dependency that is broken on rerun" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("output_tail = %v, want it to contain the setup.sh failure's own diagnostic output", lines)
	}
}

// gitRevParseHEAD returns dir's own checked-out HEAD SHA via a real `git
// rev-parse HEAD` call.
func gitRevParseHEAD(t *testing.T, dir string) string {
	t.Helper()
	shas := boot.DiscoverRepoSHAs(filepath.Dir(dir), 5*time.Second)
	sha, ok := shas[filepath.Base(dir)]
	if !ok {
		t.Fatalf("DiscoverRepoSHAs found no entry for %s", dir)
	}
	return sha
}
