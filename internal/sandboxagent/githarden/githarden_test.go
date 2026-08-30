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

// TestHardeningFlags_NeutralisesEveryRepoSettableCommandKey asserts the
// SET, not a sample.
//
// The phase audit found credential.helper missing while hooksPath and
// fsmonitor were present — a check that names some of the dangerous keys
// reads like a perimeter and is not one. credential.helper is the worst
// of them: git runs the named command AND hands it the credential over
// its own protocol, so one planted value is both execution as this
// process and theft of the SCM token.
//
// Both entry points are asserted, because they were two separate lists
// until this audit and a key added to one is exactly what gets missed.
func TestHardeningFlags_NeutralisesEveryRepoSettableCommandKey(t *testing.T) {
	// Every key here makes git RUN something and is settable from the
	// repository's own .git/config, which the agent runtime owns.
	want := map[string]string{
		"credential.helper": "",
		"core.hooksPath":    "/dev/null",
		"core.sshCommand":   "",
		"diff.external":     "",
		"core.pager":        "cat",
		"core.fsmonitor":    "",
	}

	for _, tc := range []struct {
		name string
		got  []string
	}{
		{"Args", Args("/workspace/repo", "status")},
		{"Harden", Harden([]string{"-C", "/workspace/repo", "status"})},
	} {
		set := map[string]string{}
		for i := 0; i+1 < len(tc.got); i++ {
			if tc.got[i] != "-c" {
				continue
			}
			k, v, found := strings.Cut(tc.got[i+1], "=")
			if !found {
				t.Errorf("%s: -c %q has no '='", tc.name, tc.got[i+1])
				continue
			}
			set[k] = v
		}
		for key, wantValue := range want {
			gotValue, ok := set[key]
			if !ok {
				t.Errorf("%s: %s is NOT neutralised; a repository-authored value for it runs a command as this process", tc.name, key)
				continue
			}
			if gotValue != wantValue {
				t.Errorf("%s: %s = %q, want %q", tc.name, key, gotValue, wantValue)
			}
		}
	}
}

// TestArgs_CredentialHelperResetPrecedesTheCallersOwn pins the ORDER,
// which is the whole reason the empty value works.
//
// credential.helper is multi-valued: git accumulates helpers rather than
// replacing them. An empty value discards every helper configured so far
// — including one planted in the repository's config — and the caller's
// own helper, added after, is then the only survivor. Reversed, the
// reset would wipe Narvi's own helper and leave the repository's.
func TestArgs_CredentialHelperResetPrecedesTheCallersOwn(t *testing.T) {
	args := Args("/workspace/repo", "-c", "credential.helper=!narvi credential-helper", "fetch")

	resetAt, oursAt := -1, -1
	for i, a := range args {
		if a != "-c" || i+1 >= len(args) {
			continue
		}
		switch args[i+1] {
		case "credential.helper=":
			resetAt = i
		case "credential.helper=!narvi credential-helper":
			oursAt = i
		}
	}
	if resetAt == -1 {
		t.Fatal("no credential.helper reset in the hardening flags")
	}
	if oursAt == -1 {
		t.Fatal("the caller's own credential.helper did not survive")
	}
	if resetAt > oursAt {
		t.Errorf("the reset is at %d and the caller's helper at %d: the reset must come FIRST, or it discards Narvi's own helper and leaves the repository's", resetAt, oursAt)
	}
}
