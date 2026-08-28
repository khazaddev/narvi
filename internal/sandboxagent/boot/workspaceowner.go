// This file implements this Step's own scoping-discipline requirement
// for TECHNICAL_PLAN.md §30.5's own "inventory what the runtime
// legitimately uses and preserve exactly that" rule: the workspace tree
// is named there, verbatim, as something the isolated agent runtime must
// keep working access to.

package boot

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// On the asymmetry between this and the credential itself, which is why
// only one of them needed a privilege-free guard added:
//
// Removing the Credential from the spawn fails OPEN and silently -- the
// runtime keeps working, at sandbox-agent's own uid, with the boundary
// gone and nothing to notice. That is why there is now a test asserting
// the credential reaches the kernel's attributes, runnable anywhere.
//
// Removing this chown fails CLOSED and loudly: a runtime dropped to
// another uid cannot read its own workspace, so the very first turn fails
// visibly rather than quietly proceeding without protection. A test that
// would catch its silent removal is worth less than it looks, because
// production catches it immediately and unmistakably. Recorded rather than
// scaffolded around.

// ChownWorkspaceForRuntime recursively changes the owner of every entry
// under workspaceDir to uid/gid -- the SAME uid/gid
// cmd/sandbox-agent/main.go builds the agent runtime's own *syscall.
// Credential from (boot.Config.RuntimeUID/RuntimeGID).
//
// Why this exists at all: everything under workspaceDir up to this point
// -- the clone itself (internal/sandboxagent/gitclone.CloneAll), every
// repo-configured setup hook, every services.yml command, the generated
// AGENTS.md manifest and opencode.json config -- is written by
// sandbox-agent's OWN trusted process (git clone/hooks/services all run
// via supervisor.Spec's own nil-Credential default, sandbox-agent's own
// identity; see supervisor.Spec.Credential's own doc comment for why
// that is correct, not an oversight). Left untouched, those files would
// stay owned by sandbox-agent's own uid with an ordinary umask -- world
// or group READABLE in the common case, but not group/other WRITABLE --
// which would leave the isolated runtime able to read the workspace but
// not edit files, `git commit`, or run a build, silently breaking the one
// piece of legitimate agent behavior §30.5 explicitly requires be
// preserved. This call closes that gap by re-owning the entire tree to
// the runtime's own uid/gid, once, after every boot-time writer
// (gitclone, hooks, services, the manifest writer) has already finished
// and before the WS bridge would ever let a "prompt" command reach the
// runtime -- see this function's own call site in
// cmd/sandbox-agent/main.go for the exact ordering.
//
// Lchown (never Chown/os.Chown, which follows a symlink to its target)
// is deliberate: workspaceDir's own content is CUSTOMER-CONTROLLED git
// repo content by the time this walks it, and a repo-authored symlink
// pointing outside workspaceDir (e.g. at an arbitrary absolute path) must
// never cause this call to re-own a file outside the tree it was asked
// to re-own -- Lchown changes the ownership of the symlink INODE ITSELF,
// never whatever it points at.
//
// A partial failure (one entry's Lchown erroring, e.g. a filesystem
// quirk) aborts the whole walk immediately and returns that error,
// wrapped -- this function's own caller treats any error here as fatal
// to boot (see that call site's own comment for why: a partially
// re-owned workspace is worse than a clearly-failed boot).
func ChownWorkspaceForRuntime(workspaceDir string, uid, gid uint32) error {
	err := filepath.WalkDir(workspaceDir, func(path string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		return os.Lchown(path, int(uid), int(gid))
	})
	if err != nil {
		return fmt.Errorf("boot: chown workspace %s for runtime uid=%d gid=%d: %w", workspaceDir, uid, gid, err)
	}
	return nil
}
