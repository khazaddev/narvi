package supervisor

import (
	"syscall"
	"testing"
)

// TestSysProcAttrFor_CarriesTheCredentialToTheKernel is the guard the
// §30.5 boundary would otherwise not have anywhere CI can see.
//
// The tests that prove the boundary is REAL -- a spawned child failing to
// read another uid's 0600 file, or this process's own environ -- need root
// on Linux, so they skip on a developer's machine and skip again in CI,
// which runs unprivileged on ubuntu-latest. Verified by running them: they
// report SKIP in both places. That left the whole mechanism with no
// automated protection at all; removing the credential assignment would
// have gone green everywhere it is ever checked.
//
// This test cannot prove the kernel enforces anything, and does not claim
// to. It proves the thing a regression actually breaks: the credential a
// caller supplied reaches the attributes the kernel is handed, unchanged,
// and a caller who supplied none gets no credential rather than a
// defaulted one.
func TestSysProcAttrFor_CarriesTheCredentialToTheKernel(t *testing.T) {
	cred := &syscall.Credential{Uid: 65534, Gid: 65534}

	got := sysProcAttrFor(Spec{Credential: cred})
	if got.Credential == nil {
		t.Fatal("a spec carrying a credential produced attributes with none -- the UID drop would not happen and nothing else in CI would notice")
	}
	if got.Credential.Uid != 65534 || got.Credential.Gid != 65534 {
		t.Errorf("credential = uid %d gid %d, want the caller's own 65534/65534 unchanged -- this layer must never derive or default one",
			got.Credential.Uid, got.Credential.Gid)
	}
	if !got.Setpgid {
		t.Error("Setpgid was dropped; process-group teardown depends on it")
	}
}

// TestSysProcAttrFor_NoCredentialMeansNoCredential pins the other
// direction. Every pre-existing call site passes none, and a defaulted one
// here would change how unrelated processes are spawned -- including on a
// developer machine where dropping to another uid is not permitted at all.
func TestSysProcAttrFor_NoCredentialMeansNoCredential(t *testing.T) {
	got := sysProcAttrFor(Spec{})
	if got.Credential != nil {
		t.Errorf("a spec with no credential produced one anyway (uid %d) -- this layer must not invent an identity", got.Credential.Uid)
	}
	if !got.Setpgid {
		t.Error("Setpgid was dropped")
	}
}
