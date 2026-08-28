package supervisor

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// testUnprivilegedUID/testUnprivilegedGID are the traditional "nobody"/
// "nogroup" numeric ids -- present (as raw kernel ids, no /etc/passwd
// entry required -- see Spec.Credential's own doc comment) on virtually
// every Linux system, and guaranteed unprivileged (non-zero, never
// root). Used below as a target uid/gid distinct from whatever this test
// binary itself runs as.
const (
	testUnprivilegedUID = 65534
	testUnprivilegedGID = 65534
)

// requireLinuxRoot skips t unless running on Linux AS root (euid 0):
// every test below spawns a child at an UNPRIVILEGED uid/gid, which
// itself requires the calling process to hold CAP_SETUID/CAP_SETGID (in
// practice: be root) -- see Spec.Credential's own doc comment. This
// codebase builds and tests on macOS locally and Linux in CI; on macOS
// (or on Linux as a non-root user, the common unprivileged dev/CI case)
// these tests are skipped, never faked -- see this package's own
// TestSpawn_CredentialSelfUID_Succeeds for the one Credential assertion
// this suite proves unprivileged.
func requireLinuxRoot(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("requires linux (this test asserts a real kernel-enforced file-permission denial across a uid boundary); running on %s", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		t.Skip("requires running as root (euid 0): dropping a spawned child to an UNPRIVILEGED uid/gid requires CAP_SETUID/CAP_SETGID, which a non-root test process does not have -- see Spec.Credential's own doc comment")
	}
}

// TestSpawn_CredentialSelfUID_Succeeds proves Spec.Credential is threaded
// through to the spawned child's own SysProcAttr, and that setting it to
// the CALLING process's own current uid/gid -- with NoSetGroups: true --
// does not break an ordinary spawn with no privilege required. This is
// the one Credential-related assertion this package's own test suite can
// prove BY EXECUTION in an unprivileged environment (a real dev machine,
// ordinary CI): it does NOT, and cannot, prove that the kernel actually
// enforces a DIFFERENT uid boundary -- see
// TestSpawn_CredentialDropsCannotReadAnotherUIDsFile and
// TestSpawn_CredentialDropsCannotReadThisProcesssEnviron below for that,
// both of which need real root privilege on Linux to run at all.
//
// NoSetGroups: true is deliberate and load-bearing here, not incidental:
// Go's own exec path calls setgroups() whenever Credential is non-nil
// UNLESS NoSetGroups is true, and setgroups() itself always requires
// privilege regardless of whether it changes anything -- confirmed live,
// not assumed (this test originally omitted NoSetGroups and failed with
// "operation not permitted" on an ordinary unprivileged macOS dev
// account). Production's own Credential (cmd/sandbox-agent/main.go)
// leaves NoSetGroups at its zero value (false) deliberately -- sandbox-
// agent runs privileged there, and actually clearing supplementary groups
// on the way down to the unprivileged runtime uid is exactly the least-
// privilege behavior wanted; only THIS unprivileged, self-uid test needs
// to skip that call.
func TestSpawn_CredentialSelfUID_Succeeds(t *testing.T) {
	t.Parallel()

	sup := New()
	proc, err := sup.Spawn(Spec{
		Path: "/bin/sh",
		Args: []string{"-c", "exit 0"},
		Credential: &syscall.Credential{
			Uid:         uint32(os.Getuid()),
			Gid:         uint32(os.Getgid()),
			NoSetGroups: true,
		},
	})
	if err != nil {
		t.Fatalf("Spawn() with Credential set to the caller's own uid/gid: error = %v, want nil", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, waitErr := proc.Wait(ctx)
	if waitErr != nil {
		t.Fatalf("Wait() error = %v", waitErr)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
}

// TestSpawn_NilCredential_UnchangedBehavior proves the zero value --
// every call site that existed before this field was added, and every
// call site EXCEPT opencodeproc's own runtime spawn today -- still spawns
// with no Credential set on SysProcAttr at all (only Setpgid), identical
// to this package's own pre-existing behavior.
func TestSpawn_NilCredential_UnchangedBehavior(t *testing.T) {
	t.Parallel()

	sup := New()
	proc, err := sup.Spawn(Spec{Path: "/bin/sh", Args: []string{"-c", "exit 0"}})
	if err != nil {
		t.Fatalf("Spawn() error = %v, want nil", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, waitErr := proc.Wait(ctx)
	if waitErr != nil {
		t.Fatalf("Wait() error = %v", waitErr)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
}

// TestSpawn_CredentialDropsCannotReadAnotherUIDsFile is this Step's own
// real, executed proof of kernel enforcement -- not construction: a file
// owned by this (root) test process, mode 0600 (the credential cache's
// own real permissions, internal/sandboxagent/credentials/cache.go),
// must NOT be readable by a child spawned at an unprivileged uid via
// Spec.Credential. Mirrors the real attack §30.5 closes exactly: today,
// same-UID, that file (or the sandbox bearer sitting in sandbox-agent's
// own /proc/<pid>/environ) is one read away; after this Step, a
// different-uid child gets EACCES from the kernel, regardless of what
// this codebase does or does not do at the application layer.
//
// Needs: Linux, running as root (see requireLinuxRoot) -- an unprivileged
// caller cannot ask the kernel to start a process at a DIFFERENT uid at
// all, so this property cannot be demonstrated without that privilege.
func TestSpawn_CredentialDropsCannotReadAnotherUIDsFile(t *testing.T) {
	requireLinuxRoot(t)

	dir := t.TempDir()
	secretPath := filepath.Join(dir, "narvi-credentials-like-secret.json")
	if err := os.WriteFile(secretPath, []byte(`{"password":"should-be-unreadable"}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	sup := New()
	var stdout, stderr bytes.Buffer
	proc, err := sup.Spawn(Spec{
		Path:   "/bin/cat",
		Args:   []string{secretPath},
		Stdout: &stdout,
		Stderr: &stderr,
		Credential: &syscall.Credential{
			Uid: testUnprivilegedUID,
			Gid: testUnprivilegedGID,
		},
	})
	if err != nil {
		t.Fatalf("Spawn() error = %v, want nil (starting a process AT an unprivileged uid, as root, is itself permitted)", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, waitErr := proc.Wait(ctx)
	if waitErr != nil {
		t.Fatalf("Wait() error = %v", waitErr)
	}

	if result.ExitCode == 0 {
		t.Fatalf("cat (uid %d) read a 0600 file owned by this root test process successfully (stdout=%q); want the kernel to refuse with a permission error (stderr=%q)",
			testUnprivilegedUID, stdout.String(), stderr.String())
	}
}

// TestSpawn_CredentialDropsCannotReadThisProcesssEnviron is the second,
// distinctly-named consequence §30.5 states as verified: sandbox-agent's
// own /proc/<pid>/environ (which carries NARVI_SESSION_CONFIG -- the
// sandbox bearer -- even after opencodeproc's own EnvWithout strip, since
// that strip only affects the CHILD's environment, never sandbox-agent's
// own) must become unreadable to a child running at a different uid. This
// test's "sandbox-agent" stand-in is the test process itself (running as
// root, so its own /proc/<pid>/environ is mode -r--------, readable only
// by uid 0 or a process with CAP_DAC_READ_SEARCH/CAP_DAC_OVERRIDE) --
// spawning a child at an unprivileged uid and having it try to read that
// exact path is the most direct possible reproduction of the real attack.
//
// Needs: Linux, running as root -- same reasoning as
// TestSpawn_CredentialDropsCannotReadAnotherUIDsFile above.
func TestSpawn_CredentialDropsCannotReadThisProcesssEnviron(t *testing.T) {
	requireLinuxRoot(t)

	target := filepath.Join("/proc", strconv.Itoa(os.Getpid()), "environ")

	sup := New()
	var stdout, stderr bytes.Buffer
	proc, err := sup.Spawn(Spec{
		Path:   "/bin/cat",
		Args:   []string{target},
		Stdout: &stdout,
		Stderr: &stderr,
		Credential: &syscall.Credential{
			Uid: testUnprivilegedUID,
			Gid: testUnprivilegedGID,
		},
	})
	if err != nil {
		t.Fatalf("Spawn() error = %v, want nil", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, waitErr := proc.Wait(ctx)
	if waitErr != nil {
		t.Fatalf("Wait() error = %v", waitErr)
	}

	if result.ExitCode == 0 {
		t.Fatalf("cat (uid %d) read THIS TEST PROCESS's own /proc/%d/environ successfully (stdout=%q); want the kernel to refuse with a permission error (stderr=%q) -- this is the exact env-leak §30.5 names",
			testUnprivilegedUID, os.Getpid(), stdout.String(), stderr.String())
	}
}
