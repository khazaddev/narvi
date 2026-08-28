// Package githarden supplies the flags every git command sandbox-agent
// runs must carry, and the reason they are not optional.
//
// §30.5 drops the agent runtime to its own UID and hands it the workspace,
// so the runtime owns the repositories it works in -- including each
// repository's own .git directory. Sandbox-agent still runs git in those
// same repositories, as itself, to clone, sync, read a head SHA and push.
// That leaves two problems, and only the first one announces itself.
//
// The loud one: git refuses to operate on a repository owned by another
// user ("detected dubious ownership") unless that path is declared safe.
// Without this, every push and every head-sha read fails outright.
//
// The quiet one, which is why declaring the path safe is not on its own a
// fix: a repository the runtime owns is a repository the runtime can write
// hooks into. `git push` runs .git/hooks/pre-push, so declaring the path
// safe and stopping there hands a prompt-injected agent arbitrary
// execution AS SANDBOX-AGENT -- recovering precisely the identity §30.5
// exists to take away. Verified rather than reasoned: in a container, a
// runtime-planted pre-push hook printed "HOOK RAN as uid=0" under a root
// push with only safe.directory set, and did not run once hooksPath was
// pointed away.
//
// So both go together, always, at every call site -- which is why they
// live in one function instead of being remembered eight times. Nothing
// sandbox-agent does with git ever wants a repository-authored hook or a
// repository-authored configuration value; those exist for humans working
// in their own checkouts.

package githarden

import "github.com/khazaddev/narvi/internal/sandboxagent/supervisor"

// noHooksPath points git at a location that holds no hooks and cannot be
// made to hold any. /dev/null is not a directory, so every hook lookup
// under it simply finds nothing -- no directory to create, no directory
// anyone could later write into, and no ordering question about who
// created it first.
const noHooksPath = "/dev/null"

// Args returns git's own arguments for a command operating on repoDir,
// with the hardening ahead of whatever the caller wants to run.
//
// Callers pass what they would have passed anyway; the flags this adds
// come first because git requires its -c options before the subcommand.
func Args(repoDir string, rest ...string) []string {
	args := []string{
		"-C", repoDir,

		// The repository is owned by the runtime, not by this process.
		// Scoped to this exact path rather than the wildcard: a wildcard
		// would also cover any other foreign-owned repository this
		// process is ever pointed at, including one it did not create.
		"-c", "safe.directory=" + repoDir,

		// A repository-authored hook must never execute as this process.
		"-c", "core.hooksPath=" + noHooksPath,

		// core.fsmonitor names a command git runs on ordinary operations,
		// and it is settable from the repository's own config -- which the
		// runtime owns. Emptied here so a value planted there is inert.
		"-c", "core.fsmonitor=",
	}
	return append(args, rest...)
}

// Spec builds a supervisor.Spec for a hardened git invocation. Use this
// rather than assembling one by hand: a Spec built elsewhere is a git
// command running without the flags above, and the failure mode is silent
// in one direction and destructive in the other.
func Spec(repoDir string, rest ...string) supervisor.Spec {
	return supervisor.Spec{Path: "git", Args: Args(repoDir, rest...)}
}

// Harden rewrites an already-assembled git argument list, inserting the
// flags immediately after the "-C <dir>" the caller supplied.
//
// This exists for call sites that build their arguments elsewhere and pass
// them through one shared runner: hardening the runner covers every one of
// them at once, which is the only way this stays true as callers are
// added. An argument list with no "-C <dir>" is returned unchanged and
// with no repository-scoped safe.directory -- there is no repository path
// to scope it to, and inventing a wildcard would quietly widen the very
// thing this narrows.
func Harden(args []string) []string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-C" {
			repoDir := args[i+1]
			out := make([]string, 0, len(args)+6)
			out = append(out, args[:i+2]...)
			out = append(out,
				"-c", "safe.directory="+repoDir,
				"-c", "core.hooksPath="+noHooksPath,
				"-c", "core.fsmonitor=",
			)
			return append(out, args[i+2:]...)
		}
	}
	return args
}
