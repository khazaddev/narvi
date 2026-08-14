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
		nil, noopReporter, 10*time.Second, time.Second, testReadinessTimeout, testReadinessPollInterval)
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
		nil, noopReporter, 10*time.Second, time.Second, testReadinessTimeout, testReadinessPollInterval)
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
		nil, noopReporter, 10*time.Second, time.Second, testReadinessTimeout, testReadinessPollInterval)
	if err != nil {
		t.Fatalf("RunBoot() error = %v, want nil (a non-idempotent setup.sh's rerun failure must be non-fatal -- boot still succeeds)", err)
	}

	rawTail, ok := handler.findAttr("output_tail")
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

// TestResilienceScenario_RepoAbsentFromWorkspaceMoved_SetupStillReruns proves
// workspaceMovedFor's own load-bearing "assume moved" default (hooks.go) for
// a repo genuinely ABSENT from the workspaceMoved map -- not a hand-built map
// with a key deleted, but the real production mechanism the doc comment
// names: ComputeWorkspaceMoved only ever emits an entry for a repo present
// in currentSHAs (boot.DiscoverRepoSHAs's own return shape), and
// DiscoverRepoSHAs omits a repo entirely when `git rev-parse HEAD` fails for
// it (the OTHER of the doc comment's two named causes is the repo's
// directory never existing at all under workspaceDir -- not usable here,
// since with no directory there would be no setup.sh on disk to prove
// reran).
//
// repo-no-sha's directory here is a real `git init` with zero commits: .git
// exists (so DiscoverRepoSHAs's own os.Stat(".git") gate passes and it
// actually shells out to git), but `git rev-parse HEAD` on a repo with no
// commits yet fails ("ambiguous argument 'HEAD'") -- a realistic
// production shape (e.g. a session repo cloned/initialized but not yet
// committed to) that DiscoverRepoSHAs documents itself as silently omitting.
// The resulting currentSHAs therefore has no "repo-no-sha" key at all, so
// ComputeWorkspaceMoved (which only ranges over currentSHAs) never emits a
// workspaceMoved["repo-no-sha"] entry either -- proving the omission
// genuinely propagates end to end, not merely asserted by construction.
//
// Under BootModeRepoImage, EvaluateHook's HookSetup ShouldRun IS
// workspaceMoved for that repo (hook.go) -- so if workspaceMovedFor's
// missing-key default were ever flipped from true to false, this repo's
// setup.sh would be silently skipped instead of rerun. This test is the one
// place in the suite that actually takes that missing-key branch under
// repo_image; every other repo_image test (both above in this file, and
// TestRunHooks_LogsSetupRerunDecision in hooks_test.go) passes a map that
// already contains an explicit entry for the repo under test.
func TestResilienceScenario_RepoAbsentFromWorkspaceMoved_SetupStillReruns(t *testing.T) {
	workspaceDir := t.TempDir()
	repoDir := filepath.Join(workspaceDir, "repo-no-sha")
	mkdirAll(t, repoDir)
	runGit(t, repoDir, "init") // .git exists, but zero commits: rev-parse HEAD fails

	rerunMarker := filepath.Join(workspaceDir, "setup-rerun-marker-no-sha")
	writeScript(t, filepath.Join(repoDir, "setup.sh"), "touch "+rerunMarker)

	// The real production path (cmd/sandbox-agent/main.go's own
	// runBootSequence): discover current SHAs over the whole workspace, then
	// compute workspaceMoved from that set -- never a hand-constructed map.
	currentSHAs := boot.DiscoverRepoSHAs(workspaceDir, 5*time.Second)
	if _, ok := currentSHAs["repo-no-sha"]; ok {
		t.Fatalf("precondition failed: DiscoverRepoSHAs()[repo-no-sha] present, want absent (rev-parse HEAD should fail on a zero-commit repo); got %v", currentSHAs)
	}

	manifest := boot.ImageManifest{
		Fingerprint:   "fp-repo-absent-from-sha-set",
		BuiltRepoShas: map[string]string{"repo-no-sha": "some-built-sha"},
	}
	workspaceMoved := boot.ComputeWorkspaceMoved(manifest, true, currentSHAs)
	if _, ok := workspaceMoved["repo-no-sha"]; ok {
		t.Fatalf("precondition failed: ComputeWorkspaceMoved()[repo-no-sha] present, want absent (no current SHA to compare against); got %v", workspaceMoved)
	}

	sup := supervisor.New()
	repos := []boot.RepoInfo{{Name: "repo-no-sha", Primary: true}}

	err := boot.RunBoot(context.Background(), sup, workspaceDir, repos, sandboxboot.BootModeRepoImage, workspaceMoved,
		nil, noopReporter, 10*time.Second, time.Second, testReadinessTimeout, testReadinessPollInterval)
	if err != nil {
		t.Fatalf("RunBoot() error = %v, want nil (a repo absent from workspaceMoved must still rerun setup.sh non-fatally, per the safe default)", err)
	}

	assertFileExists(t, rerunMarker)
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
