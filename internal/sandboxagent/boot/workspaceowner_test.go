package boot_test

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"github.com/narvidev/narvi/internal/sandboxagent/boot"
)

// lchownUID reports the on-disk owner of path via os.Lstat's own
// FileInfo.Sys() -- NEVER following a symlink, mirroring
// ChownWorkspaceForRuntime's own Lchown choice exactly, so this test
// observes the same thing that function changes. *syscall.Stat_t's own
// Uid field is uint32 on both linux and darwin (verified: `go doc
// syscall.Stat_t` on both GOOS) -- no build tag needed.
func lchownUID(t *testing.T, path string) uint32 {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat(%s): %v", path, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("Lstat(%s).Sys() = %T, want *syscall.Stat_t", path, info.Sys())
	}
	return st.Uid
}

// TestChownWorkspaceForRuntime_SelfUID_DoesNotError proves
// ChownWorkspaceForRuntime walks a real, nested tree and returns no error
// using the CALLING process's own current uid/gid (a self-referential
// chown, POSIX-permitted with no privilege at all). Deliberately does
// NOT assert anything about the resulting owner value: chowning a file
// to the uid/gid it ALREADY has is observably identical to never
// chowning it at all -- confirmed live (an earlier version of this test
// asserted owner-equality here and passed even after mutating this
// package's own Lchown call into a pure no-op, i.e. it was vacuous). See
// TestChownWorkspaceForRuntime_ActuallyChangesOwnership below for this
// function's own real, executed proof that ownership actually changes,
// which needs a genuinely different uid and therefore real privilege.
func TestChownWorkspaceForRuntime_SelfUID_DoesNotError(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "repo1", "src", "pkg")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	filePath := filepath.Join(nested, "main.go")
	if err := os.WriteFile(filePath, []byte("package pkg\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	selfUID := uint32(os.Getuid())
	selfGID := uint32(os.Getgid())

	if err := boot.ChownWorkspaceForRuntime(root, selfUID, selfGID); err != nil {
		t.Fatalf("ChownWorkspaceForRuntime() error = %v, want nil", err)
	}
}

// TestChownWorkspaceForRuntime_ActuallyChangesOwnership is this
// function's own real, EXECUTED proof that every entry in a nested tree
// is genuinely re-owned: run as root (uid 0), a fresh tree's every entry
// starts owned by uid 0 -- after ChownWorkspaceForRuntime(root, 65534,
// 65534), every single one (root dir, nested dirs, the file) must report
// owner 65534, a REAL, observable change unavailable to the self-uid
// test above.
//
// Needs: Linux, running as root -- an unprivileged caller cannot chown
// ANY path to a uid other than its own current one at all (POSIX:
// CAP_CHOWN required), so this property is undemonstrable without that
// privilege. Same gate/reasoning as
// internal/sandboxagent/supervisor's own requireLinuxRoot.
func TestChownWorkspaceForRuntime_ActuallyChangesOwnership(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("requires linux; running on %s", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		t.Skip("requires running as root (euid 0): chowning to a DIFFERENT uid/gid requires CAP_CHOWN, which a non-root test process does not have")
	}

	root := t.TempDir()
	nested := filepath.Join(root, "repo1", "src", "pkg")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	filePath := filepath.Join(nested, "main.go")
	if err := os.WriteFile(filePath, []byte("package pkg\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	const targetUID, targetGID = 65534, 65534
	for _, p := range []string{root, filepath.Join(root, "repo1"), nested, filePath} {
		if got := lchownUID(t, p); got != 0 {
			t.Fatalf("precondition failed: owner of %s = %d, want 0 (root) before ChownWorkspaceForRuntime runs", p, got)
		}
	}

	if err := boot.ChownWorkspaceForRuntime(root, targetUID, targetGID); err != nil {
		t.Fatalf("ChownWorkspaceForRuntime() error = %v, want nil", err)
	}

	for _, p := range []string{root, filepath.Join(root, "repo1"), nested, filePath} {
		if got := lchownUID(t, p); got != targetUID {
			t.Errorf("owner of %s = %d, want %d", p, got, uint32(targetUID))
		}
	}
}

// TestChownWorkspaceForRuntime_DoesNotFollowSymlinks is this Step's own
// real, executed proof that a repo-authored symlink pointing OUTSIDE
// workspaceDir cannot cause this function to re-own (or even touch)
// whatever it points at: Lchown changes the symlink's own inode, never
// its target -- os.Chown, by contrast, follows the link and operates on
// the target instead.
//
// The link deliberately points at a path that does NOT exist, rather
// than comparing a real target's before/after owner: this test's own
// self-uid/gid mutation would be a no-op OWNERSHIP-VALUE change either
// way (chowning a file to the uid/gid it already has), so an
// ownership-equality assertion cannot actually distinguish "never
// touched" from "touched, but to an identical value" -- confirmed live
// (an earlier version of this test compared owner values around a real
// outside file and PASSED even after deliberately mutating
// ChownWorkspaceForRuntime to os.Chown, i.e. it was vacuous). A DANGLING
// symlink instead makes the two behaviors diverge on a completely
// different, unambiguous axis: Lchown never needs to resolve the link at
// all and succeeds; os.Chown must resolve it first and fails with ENOENT
// (the target does not exist), which surfaces as a real, non-nil error
// from the whole WalkDir call. See this Step's own report for the exact
// mutation that caught the first version's vacuousness and motivated
// this rewrite.
func TestChownWorkspaceForRuntime_DoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	danglingTarget := filepath.Join(t.TempDir(), "this-path-is-never-created")

	linkPath := filepath.Join(root, "escape-link")
	if err := os.Symlink(danglingTarget, linkPath); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	selfUID := uint32(os.Getuid())
	selfGID := uint32(os.Getgid())
	if err := boot.ChownWorkspaceForRuntime(root, selfUID, selfGID); err != nil {
		t.Fatalf("ChownWorkspaceForRuntime() error = %v, want nil (Lchown must re-own the dangling symlink's own inode without ever needing to resolve its nonexistent target)", err)
	}

	// The symlink's OWN inode (not its nonexistent target, which Lstat
	// also never tries to resolve) must have been visited/re-owned, same
	// as every other entry.
	if got := lchownUID(t, linkPath); got != selfUID {
		t.Errorf("owner of the symlink itself = %d, want %d", got, selfUID)
	}
}

// TestChownWorkspaceForRuntime_NonexistentDir proves a nonexistent
// workspaceDir is a real, propagated error, never a silent success --
// this function's own caller treats any error here as fatal to boot (see
// its own doc comment), so a silently-ignored nonexistent-dir case would
// be a much harder failure to diagnose later.
func TestChownWorkspaceForRuntime_NonexistentDir(t *testing.T) {
	if err := boot.ChownWorkspaceForRuntime(filepath.Join(t.TempDir(), "does-not-exist"), 65534, 65534); err == nil {
		t.Fatal("ChownWorkspaceForRuntime() error = nil, want an error for a nonexistent workspaceDir")
	}
}
