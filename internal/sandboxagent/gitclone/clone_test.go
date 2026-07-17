package gitclone_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
	"github.com/khazaddev/narvi/internal/sandboxagent/gitclone"
	"github.com/khazaddev/narvi/internal/sandboxagent/supervisor"
)

// These tests spawn real `git` subprocesses -- git is always present on
// both macOS and Linux CI runners (§6.4's own git-based workflow assumes
// this too).

const (
	testCloneTimeout = 30 * time.Second
	testStopGrace    = 2 * time.Second
)

// runGit runs git with args in dir, failing the test immediately on any
// error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (dir=%s) failed: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

// initRepo creates a fresh git repo at dir on branch "main" with one
// commit (a README.md), configuring a throwaway local user identity so the
// commit itself never depends on any ambient git config.
func initRepo(t *testing.T, dir string) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}

	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "initial commit")
}

// currentBranch returns the checked-out branch name at dir.
func currentBranch(t *testing.T, dir string) string {
	t.Helper()

	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git -C %s rev-parse --abbrev-ref HEAD: %v", dir, err)
	}
	return strings.TrimSpace(string(out))
}

func TestCloneAll_SinglePrimarySucceeds(t *testing.T) {
	t.Parallel()

	srcDir := filepath.Join(t.TempDir(), "src")
	initRepo(t, srcDir)

	workspaceDir := t.TempDir()
	repos := []sessionconfig.SessionConfigReposElem{
		{Name: "repo1", Url: srcDir},
	}

	sup := supervisor.New()
	results, err := gitclone.CloneAll(context.Background(), sup, workspaceDir, repos, testCloneTimeout, testStopGrace)
	if err != nil {
		t.Fatalf("CloneAll() error = %v, want nil", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}

	got := results[0]
	if got.Err != nil {
		t.Errorf("results[0].Err = %v, want nil", got.Err)
	}
	if !got.Primary {
		t.Error("results[0].Primary = false, want true (position 0)")
	}

	wantDir := filepath.Join(workspaceDir, "repo1")
	if got.Dir != wantDir {
		t.Errorf("results[0].Dir = %q, want %q", got.Dir, wantDir)
	}
	if _, statErr := os.Stat(filepath.Join(wantDir, "README.md")); statErr != nil {
		t.Errorf("cloned repo missing README.md: %v", statErr)
	}
}

func TestCloneAll_ExplicitBranchChecksOutThatBranch(t *testing.T) {
	t.Parallel()

	srcDir := filepath.Join(t.TempDir(), "src")
	initRepo(t, srcDir) // default branch "main"

	runGit(t, srcDir, "checkout", "-b", "feature-x")
	if err := os.WriteFile(filepath.Join(srcDir, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatalf("write feature.txt: %v", err)
	}
	runGit(t, srcDir, "add", ".")
	runGit(t, srcDir, "commit", "-m", "feature commit")
	runGit(t, srcDir, "checkout", "main")

	branch := "feature-x"
	workspaceDir := t.TempDir()
	repos := []sessionconfig.SessionConfigReposElem{
		{Name: "repo1", Url: srcDir, Branch: &branch},
	}

	sup := supervisor.New()
	results, err := gitclone.CloneAll(context.Background(), sup, workspaceDir, repos, testCloneTimeout, testStopGrace)
	if err != nil {
		t.Fatalf("CloneAll() error = %v, want nil", err)
	}

	clonedDir := results[0].Dir
	if head := currentBranch(t, clonedDir); head != "feature-x" {
		t.Errorf("cloned repo branch = %q, want %q", head, "feature-x")
	}
	if _, statErr := os.Stat(filepath.Join(clonedDir, "feature.txt")); statErr != nil {
		t.Errorf("cloned repo missing feature.txt (wrong branch checked out?): %v", statErr)
	}
}

func TestCloneAll_NilBranchClonesDefaultBranch(t *testing.T) {
	t.Parallel()

	srcDir := filepath.Join(t.TempDir(), "src")
	initRepo(t, srcDir) // default branch "main"

	workspaceDir := t.TempDir()
	repos := []sessionconfig.SessionConfigReposElem{
		{Name: "repo1", Url: srcDir}, // Branch left nil
	}

	sup := supervisor.New()
	results, err := gitclone.CloneAll(context.Background(), sup, workspaceDir, repos, testCloneTimeout, testStopGrace)
	if err != nil {
		t.Fatalf("CloneAll() error = %v, want nil", err)
	}

	if head := currentBranch(t, results[0].Dir); head != "main" {
		t.Errorf("cloned repo branch = %q, want %q (the source's own default, no --branch flag)", head, "main")
	}
}

// TestCloneAll_PrimaryFailureStopsImmediately proves a fatal primary
// failure stops CloneAll before a LATER repo is ever even attempted --
// proven by that repo's target directory never existing at all.
func TestCloneAll_PrimaryFailureStopsImmediately(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()
	repos := []sessionconfig.SessionConfigReposElem{
		{Name: "bad-primary", Url: "/nonexistent/path/to/nowhere-xyz"},
		{Name: "never-attempted", Url: "https://example.invalid/never.git"},
	}

	sup := supervisor.New()
	results, err := gitclone.CloneAll(context.Background(), sup, workspaceDir, repos, testCloneTimeout, testStopGrace)
	if err == nil {
		t.Fatal("CloneAll() error = nil, want a fatal error for the failed primary repo")
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1 (only the primary should have been attempted)", len(results))
	}
	if results[0].Err == nil {
		t.Error("results[0].Err = nil, want a clone error")
	}

	if _, statErr := os.Stat(filepath.Join(workspaceDir, "never-attempted")); !os.IsNotExist(statErr) {
		t.Errorf("second repo's directory stat = %v, want IsNotExist (repo never attempted)", statErr)
	}
}

// TestCloneAll_SecondaryFailureContinues proves a secondary repo's clone
// failure is a logged warning, not fatal -- CloneAll returns nil and a
// LATER repo after it still gets cloned.
func TestCloneAll_SecondaryFailureContinues(t *testing.T) {
	t.Parallel()

	primarySrc := filepath.Join(t.TempDir(), "primary-src")
	initRepo(t, primarySrc)

	laterSrc := filepath.Join(t.TempDir(), "later-src")
	initRepo(t, laterSrc)

	workspaceDir := t.TempDir()
	repos := []sessionconfig.SessionConfigReposElem{
		{Name: "primary", Url: primarySrc},
		{Name: "bad-secondary", Url: "/nonexistent/path/to/nowhere-xyz"},
		{Name: "later", Url: laterSrc},
	}

	sup := supervisor.New()
	results, err := gitclone.CloneAll(context.Background(), sup, workspaceDir, repos, testCloneTimeout, testStopGrace)
	if err != nil {
		t.Fatalf("CloneAll() error = %v, want nil (a secondary failure is a warning, not fatal)", err)
	}
	if len(results) != 3 {
		t.Fatalf("len(results) = %d, want 3 (every repo attempted)", len(results))
	}

	if results[0].Err != nil {
		t.Errorf("results[0] (primary) Err = %v, want nil", results[0].Err)
	}
	if results[1].Err == nil {
		t.Error("results[1] (bad secondary) Err = nil, want a clone error")
	}
	if results[2].Err != nil {
		t.Errorf("results[2] (later) Err = %v, want nil -- loop must continue past the secondary failure", results[2].Err)
	}
	if _, statErr := os.Stat(filepath.Join(workspaceDir, "later", "README.md")); statErr != nil {
		t.Errorf("later repo not actually cloned: %v", statErr)
	}
}

func TestWriteAgentsManifest(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()
	branch := "feature-x"
	results := []gitclone.CloneResult{
		{
			Repo:    sessionconfig.SessionConfigReposElem{Name: "primary-repo"},
			Primary: true,
			Dir:     filepath.Join(workspaceDir, "primary-repo"),
		},
		{
			Repo:    sessionconfig.SessionConfigReposElem{Name: "secondary-repo", Branch: &branch},
			Primary: false,
			Dir:     filepath.Join(workspaceDir, "secondary-repo"),
		},
		{
			Repo:    sessionconfig.SessionConfigReposElem{Name: "failed-repo"},
			Primary: false,
			Dir:     filepath.Join(workspaceDir, "failed-repo"),
			Err:     errors.New("clone failed"),
		},
	}

	if err := gitclone.WriteAgentsManifest(workspaceDir, results); err != nil {
		t.Fatalf("WriteAgentsManifest() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(workspaceDir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	content := string(data)

	for _, want := range []string{"primary-repo", "primary", "secondary-repo", "feature-x", "(default)"} {
		if !strings.Contains(content, want) {
			t.Errorf("manifest missing %q; content:\n%s", want, content)
		}
	}
	if strings.Contains(content, "failed-repo") {
		t.Errorf("manifest includes failed-repo, want it skipped; content:\n%s", content)
	}
}
