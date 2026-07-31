package boot_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/khazaddev/narvi/internal/domain/sandboxboot"
	"github.com/khazaddev/narvi/internal/sandboxagent/boot"
	"github.com/khazaddev/narvi/internal/sandboxagent/supervisor"
)

func writeScript(t *testing.T, path, body string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	content := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected marker file %s to exist, stat error: %v", path, err)
	}
}

func assertFileAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Errorf("expected marker file %s to be absent, but it exists", path)
	}
}

func TestRunHooks_EmptyRepos(t *testing.T) {
	t.Parallel()

	sup := supervisor.New()
	err := boot.RunHooks(context.Background(), sup, t.TempDir(), nil, sandboxboot.BootModeFresh, nil, 5*time.Second, time.Second)
	if err != nil {
		t.Fatalf("RunHooks() error = %v, want nil for an empty repos slice", err)
	}
}

func TestRunHooks_AbsentHookSkipped(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspaceDir, "repo-a"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// No setup.sh/start.sh created at all -- a repo without either script
	// is normal and expected.

	sup := supervisor.New()
	repos := []boot.RepoInfo{{Name: "repo-a", Primary: true}}

	err := boot.RunHooks(context.Background(), sup, workspaceDir, repos, sandboxboot.BootModeFresh, nil, 5*time.Second, time.Second)
	if err != nil {
		t.Fatalf("RunHooks() error = %v, want nil (absent hooks are a routine no-op)", err)
	}
}

func TestRunHooks_SuccessContinuesToLaterHooksAndRepos(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()
	markerA := filepath.Join(workspaceDir, "marker-a-setup")
	markerB := filepath.Join(workspaceDir, "marker-b-start")

	writeScript(t, filepath.Join(workspaceDir, "repo-a", "setup.sh"), "touch "+markerA)
	writeScript(t, filepath.Join(workspaceDir, "repo-b", "start.sh"), "touch "+markerB)

	sup := supervisor.New()
	repos := []boot.RepoInfo{
		{Name: "repo-a", Primary: true},
		{Name: "repo-b", Primary: false},
	}

	// fresh mode: repo-a's setup.sh runs (non-fatal); repo-b's start.sh
	// runs (non-fatal, secondary). Both succeed, so RunHooks must run
	// every hook across both repos.
	err := boot.RunHooks(context.Background(), sup, workspaceDir, repos, sandboxboot.BootModeFresh, nil, 5*time.Second, time.Second)
	if err != nil {
		t.Fatalf("RunHooks() error = %v, want nil", err)
	}

	assertFileExists(t, markerA)
	assertFileExists(t, markerB)
}

func TestRunHooks_FatalFailureStopsImmediately(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()
	laterMarker := filepath.Join(workspaceDir, "later-marker")

	// build mode: repo-a's setup.sh is fatal (primary or not -- setup.sh's
	// FatalOnFailure never depends on primary). Make it fail.
	writeScript(t, filepath.Join(workspaceDir, "repo-a", "setup.sh"), "exit 1")
	// A hook that would only run if RunHooks kept going after repo-a's
	// fatal failure.
	writeScript(t, filepath.Join(workspaceDir, "repo-b", "setup.sh"), "touch "+laterMarker)

	sup := supervisor.New()
	repos := []boot.RepoInfo{
		{Name: "repo-a", Primary: true},
		{Name: "repo-b", Primary: false},
	}

	err := boot.RunHooks(context.Background(), sup, workspaceDir, repos, sandboxboot.BootModeBuild, nil, 5*time.Second, time.Second)
	if err == nil {
		t.Fatal("RunHooks() error = nil, want an error (build mode's setup.sh failure is fatal)")
	}

	assertFileAbsent(t, laterMarker)
}

func TestRunHooks_NonFatalFailureContinues(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()
	laterMarker := filepath.Join(workspaceDir, "later-marker")

	// fresh mode: repo-a is a SECONDARY repo, so its start.sh failing is
	// only a warning, never fatal.
	writeScript(t, filepath.Join(workspaceDir, "repo-a", "start.sh"), "exit 1")
	writeScript(t, filepath.Join(workspaceDir, "repo-b", "start.sh"), "touch "+laterMarker)

	sup := supervisor.New()
	repos := []boot.RepoInfo{
		{Name: "repo-a", Primary: false},
		{Name: "repo-b", Primary: false},
	}

	err := boot.RunHooks(context.Background(), sup, workspaceDir, repos, sandboxboot.BootModeFresh, nil, 5*time.Second, time.Second)
	if err != nil {
		t.Fatalf("RunHooks() error = %v, want nil (a secondary repo's start.sh failure is only a warning)", err)
	}

	assertFileExists(t, laterMarker)
}

// TestRunHooks_EnvExcludesSessionConfig proves the real regression this
// Step's env-leak remediation fixes: a repo's own setup.sh must NOT
// inherit NARVI_SESSION_CONFIG (the sandbox's own plaintext bearer token).
// The hook script itself checks its own environment and exits 9 (a real,
// observed failure) if the var is present at all; build mode's setup.sh
// policy is fatal, so RunHooks returning nil here is proof positive the
// spawned script never saw it -- not merely that nothing crashed.
func TestRunHooks_EnvExcludesSessionConfig(t *testing.T) {
	// Not t.Parallel(): t.Setenv forbids combining the two.
	t.Setenv("NARVI_SESSION_CONFIG", "marker-should-not-reach-child")

	workspaceDir := t.TempDir()
	writeScript(t, filepath.Join(workspaceDir, "repo-a", "setup.sh"), `[ -z "$NARVI_SESSION_CONFIG" ] || exit 9`)

	sup := supervisor.New()
	repos := []boot.RepoInfo{{Name: "repo-a", Primary: true}}

	err := boot.RunHooks(context.Background(), sup, workspaceDir, repos, sandboxboot.BootModeBuild, nil, 5*time.Second, time.Second)
	if err != nil {
		t.Fatalf("RunHooks() error = %v, want nil (the spawned setup.sh must not see NARVI_SESSION_CONFIG)", err)
	}
}

func TestRunHooks_TimeoutIsFatalWhenPolicyFatal(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()
	writeScript(t, filepath.Join(workspaceDir, "repo-a", "setup.sh"), "sleep 30")

	sup := supervisor.New()
	repos := []boot.RepoInfo{{Name: "repo-a", Primary: true}}

	err := boot.RunHooks(context.Background(), sup, workspaceDir, repos, sandboxboot.BootModeBuild, nil,
		200*time.Millisecond, 200*time.Millisecond)
	if err == nil {
		t.Fatal("RunHooks() error = nil, want an error (hook exceeded hookTimeout, fatal per build mode's setup.sh policy)")
	}
}

// TestRunHooks_LogsSetupRerunDecision proves the fix for the observability
// finding: the §19.4 setup-rerun decision (repo, boot_mode, workspace_moved,
// and what was actually decided) was never logged at all -- "working as
// designed, the repo moved" was indistinguishable from "the build service
// stopped baking manifests". No script is even created on disk here: the
// decision must be logged BEFORE hookScriptPresent is ever consulted (the
// decision itself is what determines whether setup.sh would even be
// attempted, not the other way around).
//
// Deliberately NOT t.Parallel(): swaps the process-wide slog default logger
// (platform.Logger(ctx) always reads slog.Default()) to capture it -- same
// justification as gitclone/sync_test.go's own identical precedent (Go's own
// test scheduler never interleaves a non-parallel test's body with any
// other test's body in the same package).
func TestRunHooks_LogsSetupRerunDecision(t *testing.T) {
	originalLogger := slog.Default()
	var logBuf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logBuf, nil)))
	defer slog.SetDefault(originalLogger)

	workspaceDir := t.TempDir()
	sup := supervisor.New()
	repos := []boot.RepoInfo{{Name: "repo-decision-test", Primary: true}}
	workspaceMoved := map[string]bool{"repo-decision-test": true}

	if err := boot.RunHooks(context.Background(), sup, workspaceDir, repos, sandboxboot.BootModeRepoImage, workspaceMoved,
		5*time.Second, time.Second); err != nil {
		t.Fatalf("RunHooks() error = %v, want nil", err)
	}

	logged := logBuf.String()
	for _, want := range []string{
		"boot: setup-rerun decision",
		`"repo":"repo-decision-test"`,
		`"boot_mode":"repo_image"`,
		`"workspace_moved":true`,
		`"setup_will_run":true`,
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("log output = %q, want it to contain %q", logged, want)
		}
	}
}
