// Package supervisor implements the generic process-group supervisor named
// verbatim in the plan row: "native supervision (process groups, killpg,
// reaping, drain, bounded shutdown)". It runs an arbitrary command in its
// own process group and lets a caller stop it gracefully-then-forcefully,
// bounded end to end. It knows NOTHING about hooks, repos, or boot modes --
// that decision logic lives in internal/domain/sandboxboot and
// internal/sandboxagent/boot, which import this package, never the other
// way around. §14.2 ("sandbox-agent: services.yml") reuses this package
// directly for its own, unrelated long-running services -- "supervised by
// the SAME process-group/reap/drain machinery as §6.4 (no new
// supervision code)".
//
// Mechanics:
//
//   - Spawn starts the command with SysProcAttr{Setpgid: true}, so its own
//     pid becomes a new process group's leader -- any subprocess it forks
//     (e.g. a shell script backgrounding a child) inherits that same
//     group, and a single signal to the group reaches all of them, not just
//     the direct child.
//   - Stopping a group signals the NEGATIVE of the leader's pid
//     (syscall.Kill(-pgid, sig)) -- POSIX killpg semantics. syscall.ESRCH
//     (no such process -- it already exited) is treated as a benign no-op,
//     never propagated as an error.
//   - Reaping is automatic and unconditional: Spawn launches exactly one
//     background goroutine (via the Supervisor's own errgroup.Group,
//     never a bare `go` statement) that calls cmd.Wait() exactly once and
//     records the result before closing a "done" channel -- so a process
//     that exits on its own is always reaped promptly (no zombies),
//     whether or not any caller ever calls Wait or Stop.
//   - Stop(ctx, grace) is graceful-then-forceful and bounded: SIGTERM the
//     group, wait up to grace (or until ctx is done, whichever comes
//     first) for the process to exit on its own; if neither happens in
//     time, SIGKILL the group and then block unconditionally on the done
//     channel -- SIGKILL cannot be caught or ignored, so this final wait is
//     provably bounded by the OS, not by this code.
//
// Supervisor.StopAll's own internal fan-out (one goroutine per tracked
// process) uses a deliberately ZERO-VALUE errgroup.Group, exactly like
// internal/app/sessionactor/registry.go's own `group` field: if one
// process's Stop call returns a genuine error, that must never cancel a
// shared context and truncate a SIBLING process's own independent grace
// period. Each process's grace period is its own bounded thing, unrelated
// to whether a sibling's stop attempt succeeded -- the same reasoning
// applied to OS processes here that registry.go applies to session actors.
package supervisor
