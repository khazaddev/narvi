// Package gitclone implements §6.4's "Multi-repo ordered clones under
// /workspace/{name} + generated AGENTS.md manifest": CloneAll clones every
// repo named in a SESSION_CONFIG document's Repos list, IN ORDER, into
// workspaceDir/<name>, each spawned as a real `git clone` subprocess via
// the shared internal/sandboxagent/supervisor.Supervisor (the same
// process-group/reap/drain machinery Step 13/14 already use for hooks and
// services.yml -- never a bare exec.Command); WriteAgentsManifest then
// renders the successfully-cloned subset as a plain markdown manifest at
// workspaceDir/AGENTS.md.
//
// Criticality semantics mirror internal/sandboxagent/boot.RunHooks
// (Step 13) and internal/sandboxagent/services.Run (Step 14) exactly:
// repos[0] (§3.4: "position 0 = primary") failing to clone is fatal and
// stops the whole sequence immediately -- no repo after it is even
// attempted; a secondary repo's clone failure is logged as a warning
// (platform.Logger(ctx)) and the loop continues to the next repo.
//
// Every clone is configured with a PER-INVOCATION (never global) git
// credential helper: `-c credential.helper=!'<this binary's own absolute
// path>' credential-helper`, so git shells out to THIS SAME sandbox-agent
// binary's own "credential-helper" subcommand (see
// internal/sandboxagent/credentials) whenever it needs a username/password
// for an https remote -- never a long-lived credential on disk outside
// that package's own flock-protected, expiry-bounded cache (§5.2).
//
// A nil Repo.Branch (§3.4's own documented "nil means the repo's own
// default branch") means CloneAll passes NO --branch flag at all -- it
// never invents or creates a branch; that is explicitly Step 29's
// (internal/domain/gitstate's) job, not this package's.
//
// WriteAgentsManifest's exact markdown shape is this Step's own invented,
// documented convention -- no contracts/ schema governs it, exactly like
// Step 14 documented its own invented Readiness.Health shape.
package gitclone
