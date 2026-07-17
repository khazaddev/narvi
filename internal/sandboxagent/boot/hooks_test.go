package boot_test

import (
	"context"
	"os"
	"path/filepath"
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
	err := boot.RunHooks(context.Background(), sup, t.TempDir(), nil, sandboxboot.BootModeFresh, 5*time.Second, time.Second)
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

	err := boot.RunHooks(context.Background(), sup, workspaceDir, repos, sandboxboot.BootModeFresh, 5*time.Second, time.Second)
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
	err := boot.RunHooks(context.Background(), sup, workspaceDir, repos, sandboxboot.BootModeFresh, 5*time.Second, time.Second)
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

	err := boot.RunHooks(context.Background(), sup, workspaceDir, repos, sandboxboot.BootModeBuild, 5*time.Second, time.Second)
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

	err := boot.RunHooks(context.Background(), sup, workspaceDir, repos, sandboxboot.BootModeFresh, 5*time.Second, time.Second)
	if err != nil {
		t.Fatalf("RunHooks() error = %v, want nil (a secondary repo's start.sh failure is only a warning)", err)
	}

	assertFileExists(t, laterMarker)
}

func TestRunHooks_TimeoutIsFatalWhenPolicyFatal(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()
	writeScript(t, filepath.Join(workspaceDir, "repo-a", "setup.sh"), "sleep 30")

	sup := supervisor.New()
	repos := []boot.RepoInfo{{Name: "repo-a", Primary: true}}

	err := boot.RunHooks(context.Background(), sup, workspaceDir, repos, sandboxboot.BootModeBuild,
		200*time.Millisecond, 200*time.Millisecond)
	if err == nil {
		t.Fatal("RunHooks() error = nil, want an error (hook exceeded hookTimeout, fatal per build mode's setup.sh policy)")
	}
}
