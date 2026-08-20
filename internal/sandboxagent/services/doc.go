// Package services implements §14.2's own execution half of §14.2's
// multi-service boot manifest: given an already-parsed
// internal/domain/servicemanifest.Manifest, spawn every declared service
// CONCURRENTLY under the shared internal/sandboxagent/supervisor.
// Supervisor (the SAME process-group/reap/drain machinery
// internal/sandboxagent/boot already uses for setup.sh/start.sh -- "no new
// supervision code", §14.2, verbatim), wait for each one's own readiness,
// and report a sequence of named boot_progress phases (§6.1) per service
// via a plain callback.
//
// Locate/Load handle the manifest's own disk half (finding and reading
// <repoDir>/.narvi/services.yml); Run is the concurrent spawn+readiness+
// criticality orchestration; internal/sandboxagent/boot.RunBoot (a sibling
// package) is the top-level per-repo dispatcher that decides whether to
// call this package's Run or fall back to the existing hook logic.
//
// HONEST GAP (documented the same way §6.4 documented
// NARVI_IMAGE_DIGEST's own gap): ProgressReporter is a plain in-process
// callback, not a real transport. §6.1 (control-plane WS bridge) is
// expected to supply a reporter that forwards boot_progress events over
// the real WS connection once that bridge exists; until then, main.go
// wires a slog-only reporter that just logs each event.
//
// Foreground-only cmd assumption: every services.yml `cmd` is assumed to
// run in the FOREGROUND for the service's entire lifetime, exactly like
// §14.2's own two examples ("pnpm dev", "prism mock ... -p 4000") -- neither
// daemonizes. Because of this, this package treats ANY process exit before
// readiness -- even a clean exit(0) -- as a crash (PhaseFailed): there is
// no way to distinguish "a well-behaved daemon that forked and let its
// tracked leader exit cleanly" from "a script that finished setup and quit
// without ever starting the service" without a much richer daemonization
// contract, which is explicitly out of scope here. A command that forks
// and detaches a long-running child while its own tracked leader exits is
// unsupported by this design and will be reported as a crash, correctly
// per this Step's own stated scope boundary.
//
// This package does not add ongoing liveness monitoring after a service
// first becomes ready -- no heartbeats, no restart policy -- only the
// boot-time readiness wait (§14.2's own scope). Once ready (or once this
// package gives up on a secondary service after a timeout), a service is
// tracked by the shared Supervisor like any other supervised process, and
// torn down only by the existing StopAll-based shutdown path
// (cmd/sandbox-agent/main.go, already wired in §6.4) -- this package
// never calls Stop/StopAll itself.
package services
