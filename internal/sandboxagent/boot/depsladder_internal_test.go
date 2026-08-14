package boot

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mkdirAllInternal/initGitRepoInternal/runGitInternal duplicate
// fingerprint_test.go's own boot_test-package helpers of almost the same
// name -- a DIFFERENT Go package (boot vs boot_test) even though it lives
// in the same directory, so nothing there is visible here; duplicated
// rather than shared, exactly like runboot_test.go's own freePort
// precedent already establishes for this identical situation.
func mkdirAllInternal(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
}

func runGitInternal(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func initGitRepoInternal(t *testing.T, dir string) {
	t.Helper()
	runGitInternal(t, dir, "init")
	runGitInternal(t, dir, "config", "user.email", "test@example.com")
	runGitInternal(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGitInternal(t, dir, "add", "file.txt")
	runGitInternal(t, dir, "commit", "-m", "initial commit")
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestComputeDependencyManifestDigest_EmptyRepoIsDeterministic proves a
// repo with ZERO recognized lockfiles present still produces a
// well-defined, deterministic digest (never an error) -- §19.6's own
// "no package manager in use" case, not a failure.
func TestComputeDependencyManifestDigest_EmptyRepoIsDeterministic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	got1, err := ComputeDependencyManifestDigest(dir)
	if err != nil {
		t.Fatalf("ComputeDependencyManifestDigest() error = %v, want nil", err)
	}
	got2, err := ComputeDependencyManifestDigest(dir)
	if err != nil {
		t.Fatalf("ComputeDependencyManifestDigest() error = %v, want nil", err)
	}
	if got1 != got2 {
		t.Errorf("digest not deterministic: %q != %q", got1, got2)
	}
	if !isRecognizedDigestHex(got1) {
		t.Errorf("digest %q is not a recognized hex-encoded SHA-256 digest", got1)
	}
}

// TestComputeDependencyManifestDigest_ContentChangeChangesDigest is the
// MUTATION-detecting core of §19.6's first bullet: two repos differing
// ONLY in one lockfile's own byte content must produce different digests --
// otherwise the skip tier could wrongly treat a genuine dependency bump as
// unchanged.
func TestComputeDependencyManifestDigest_ContentChangeChangesDigest(t *testing.T) {
	t.Parallel()

	dirA := t.TempDir()
	dirB := t.TempDir()
	if err := os.WriteFile(filepath.Join(dirA, "package-lock.json"), []byte(`{"lockfileVersion":1}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "package-lock.json"), []byte(`{"lockfileVersion":2}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	digestA, err := ComputeDependencyManifestDigest(dirA)
	if err != nil {
		t.Fatalf("ComputeDependencyManifestDigest(dirA) error = %v", err)
	}
	digestB, err := ComputeDependencyManifestDigest(dirB)
	if err != nil {
		t.Fatalf("ComputeDependencyManifestDigest(dirB) error = %v", err)
	}
	if digestA == digestB {
		t.Errorf("digests equal for different lockfile content: %q", digestA)
	}
}

// TestComputeDependencyManifestDigest_PresenceMattersNotJustContent proves
// dropping a lockfile entirely changes the digest even though every
// REMAINING file's own content is byte-identical -- otherwise deleting a
// package manager's lockfile (a genuine dependency-surface change) could be
// silently invisible to the skip tier.
func TestComputeDependencyManifestDigest_PresenceMattersNotJustContent(t *testing.T) {
	t.Parallel()

	dirWith := t.TempDir()
	dirWithout := t.TempDir()
	if err := os.WriteFile(filepath.Join(dirWith, "go.sum"), []byte("same-content"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// dirWithout has no go.sum at all.

	digestWith, err := ComputeDependencyManifestDigest(dirWith)
	if err != nil {
		t.Fatalf("ComputeDependencyManifestDigest(dirWith) error = %v", err)
	}
	digestWithout, err := ComputeDependencyManifestDigest(dirWithout)
	if err != nil {
		t.Fatalf("ComputeDependencyManifestDigest(dirWithout) error = %v", err)
	}
	if digestWith == digestWithout {
		t.Errorf("digests equal despite go.sum being present in one and absent in the other: %q", digestWith)
	}
}

// TestComputeDependencyManifestDigest_UnreadableFilePropagatesError proves
// a genuine I/O failure (not "does not exist") on a lockfile that DOES
// exist is returned as a real error -- §19.6's own "unreadable... digest...
// means ineligible" case depends on this error actually surfacing.
func TestComputeDependencyManifestDigest_UnreadableFilePropagatesError(t *testing.T) {
	t.Parallel()

	if os.Getuid() == 0 {
		t.Skip("running as root -- file permissions are not enforced")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "go.sum")
	if err := os.WriteFile(path, []byte("content"), 0o000); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	_, err := ComputeDependencyManifestDigest(dir)
	if err == nil {
		t.Fatal("ComputeDependencyManifestDigest() error = nil, want a real error for an unreadable lockfile")
	}
}

func TestIsRecognizedDigestHex(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"valid sha256 hex", sha256Hex([]byte("anything")), true},
		{"empty string", "", false},
		{"too short", "abcd", false},
		{"right length but uppercase", strings.ToUpper(sha256Hex([]byte("anything"))), false},
		{"right length but non-hex char", "g" + sha256Hex([]byte("x"))[1:], false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isRecognizedDigestHex(tc.in); got != tc.want {
				t.Errorf("isRecognizedDigestHex(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestEvaluateDependencySkip is the pure decision table behind
// DependencySkipOutcome -- every branch of §19.6's own "unreadable, absent,
// or unrecognized... means ineligible" rule, plus the match/mismatch split.
func TestEvaluateDependencySkip(t *testing.T) {
	t.Parallel()

	validDigest := sha256Hex([]byte("baked"))
	otherDigest := sha256Hex([]byte("current"))

	tests := []struct {
		name          string
		manifestFound bool
		bakedDigest   string
		bakedOK       bool
		currentDigest string
		currentErr    error
		want          DependencySkipOutcome
	}{
		{"manifest not found", false, validDigest, true, validDigest, nil, DependencySkipIneligible},
		{"repo has no baked entry", true, "", false, validDigest, nil, DependencySkipIneligible},
		{"baked digest not recognized hex", true, "not-a-digest", true, validDigest, nil, DependencySkipIneligible},
		{"current digest compute error", true, validDigest, true, "", errComputeFailed, DependencySkipIneligible},
		{"digests match", true, validDigest, true, validDigest, nil, DependencySkipMatch},
		{"digests mismatch", true, validDigest, true, otherDigest, nil, DependencySkipMismatch},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := evaluateDependencySkip(tc.manifestFound, tc.bakedDigest, tc.bakedOK, tc.currentDigest, tc.currentErr)
			if got != tc.want {
				t.Errorf("evaluateDependencySkip(%+v) = %q, want %q", tc, got, tc.want)
			}
		})
	}
}

var errComputeFailed = &staticError{"boom"}

type staticError struct{ msg string }

func (e *staticError) Error() string { return e.msg }

// TestSetupUnchangedSinceBuild_Unchanged proves the happy path: setup.sh
// byte-identical between the built SHA and HEAD reports (true, nil).
func TestSetupUnchangedSinceBuild_Unchanged(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mkdirAllInternal(t, dir)
	initGitRepoInternal(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "setup.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGitInternal(t, dir, "add", "setup.sh")
	runGitInternal(t, dir, "commit", "-m", "add setup.sh")
	builtSHA := gitHeadInternal(t, dir)

	// A later, unrelated commit that does NOT touch setup.sh.
	if err := os.WriteFile(filepath.Join(dir, "other.txt"), []byte("other"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGitInternal(t, dir, "add", "other.txt")
	runGitInternal(t, dir, "commit", "-m", "unrelated change")

	unchanged, err := setupUnchangedSinceBuild(dir, builtSHA, 5*time.Second)
	if err != nil {
		t.Fatalf("setupUnchangedSinceBuild() error = %v, want nil", err)
	}
	if !unchanged {
		t.Error("setupUnchangedSinceBuild() = false, want true (setup.sh was never touched since builtSHA)")
	}
}

// TestSetupUnchangedSinceBuild_Changed proves a REAL setup.sh change since
// builtSHA reports (false, nil) -- a clean, definitive "no", not an error.
func TestSetupUnchangedSinceBuild_Changed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mkdirAllInternal(t, dir)
	initGitRepoInternal(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "setup.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGitInternal(t, dir, "add", "setup.sh")
	runGitInternal(t, dir, "commit", "-m", "add setup.sh")
	builtSHA := gitHeadInternal(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "setup.sh"), []byte("#!/bin/sh\necho changed\n"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGitInternal(t, dir, "add", "setup.sh")
	runGitInternal(t, dir, "commit", "-m", "change setup.sh")

	unchanged, err := setupUnchangedSinceBuild(dir, builtSHA, 5*time.Second)
	if err != nil {
		t.Fatalf("setupUnchangedSinceBuild() error = %v, want nil (a real diff is not an error)", err)
	}
	if unchanged {
		t.Error("setupUnchangedSinceBuild() = true, want false (setup.sh WAS changed since builtSHA)")
	}
}

// TestSetupUnchangedSinceBuild_UnresolvableSHAIsAnError proves §19.6's own
// "any git error on this check is conservative: ineligible" rule: a
// builtSHA git cannot resolve at all (not a real diff, a genuine failure)
// must surface as a non-nil error, never silently treated as "unchanged".
func TestSetupUnchangedSinceBuild_UnresolvableSHAIsAnError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mkdirAllInternal(t, dir)
	initGitRepoInternal(t, dir)

	_, err := setupUnchangedSinceBuild(dir, "0000000000000000000000000000000000000000", 5*time.Second)
	if err == nil {
		t.Fatal("setupUnchangedSinceBuild() error = nil, want a real error for an unresolvable builtSHA")
	}
}

func gitHeadInternal(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}
