package githarden

import (
	"slices"
	"strings"
	"testing"
)

// TestArgs_CarriesBothHalves pins the pair that must never be separated.
//
// safe.directory alone un-breaks git against a runtime-owned repository
// AND hands a prompt-injected agent execution as sandbox-agent, because
// git push runs .git/hooks/pre-push and the runtime owns .git. Verified in
// a container: with only safe.directory, a planted hook printed "HOOK RAN
// as uid=0" under a root push; with hooksPath pointed away, the same push
// succeeded and the hook did not run.
//
// So a change that keeps one and drops the other is either a broken push
// or a root execution primitive, and neither announces itself in a way a
// reviewer would notice. This test is what notices.
func TestArgs_CarriesBothHalves(t *testing.T) {
	got := Args("/workspace/repo", "push", "--", "origin", "main")

	assertFlag(t, got, "safe.directory=/workspace/repo",
		"git refuses a repository owned by another user without it, so every push and head-sha read fails")
	assertFlag(t, got, "core.hooksPath=/dev/null",
		"without it a hook the runtime planted in .git/hooks runs as this process")
	assertFlag(t, got, "core.fsmonitor=",
		"core.fsmonitor names a command git runs, and it is settable from the repository config the runtime owns")

	if got[0] != "-C" || got[1] != "/workspace/repo" {
		t.Errorf("args do not start with -C <dir>: %v", got[:2])
	}
	if !slices.Contains(got, "push") {
		t.Errorf("the caller's own arguments were dropped: %v", got)
	}
}

// TestHarden_InsertsAroundAnExistingDashC covers the call sites that
// assemble their arguments elsewhere and pass them through one runner.
func TestHarden_InsertsAroundAnExistingDashC(t *testing.T) {
	got := Harden([]string{"-C", "/workspace/repo", "status", "--porcelain"})

	assertFlag(t, got, "safe.directory=/workspace/repo", "the repository path must be scoped from the caller's own -C")
	assertFlag(t, got, "core.hooksPath=/dev/null", "hardening must not depend on which call site assembled the arguments")
	if got[len(got)-1] != "--porcelain" || !slices.Contains(got, "status") {
		t.Errorf("the caller's own subcommand was lost or reordered: %v", got)
	}
}

// TestHarden_NoRepositoryScopeIsLeftAlone: an invocation naming no
// repository has no path to declare safe, and inventing a wildcard would
// widen exactly what this narrows.
func TestHarden_NoRepositoryScopeIsLeftAlone(t *testing.T) {
	in := []string{"--version"}
	got := Harden(in)
	if !slices.Equal(got, in) {
		t.Errorf("Harden(%v) = %v, want it unchanged -- no -C means no repository to scope to", in, got)
	}
	for _, a := range got {
		if strings.HasPrefix(a, "safe.directory=") {
			t.Errorf("a wildcard-ish safe.directory was added with no repository in sight: %v", got)
		}
	}
}

func assertFlag(t *testing.T, args []string, want, why string) {
	t.Helper()
	for i, a := range args {
		if a == "-c" && i+1 < len(args) && args[i+1] == want {
			return
		}
	}
	t.Errorf("missing -c %s\n    %s\n    got: %v", want, why, args)
}
