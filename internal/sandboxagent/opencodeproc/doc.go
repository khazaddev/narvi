// Package opencodeproc spawns and supervises the `opencode serve` process
// this Step's OpenCode adapter (internal/adapters/outbound/opencode) talks
// to over HTTP+SSE. It is a small, sandbox-agent-side sibling to
// internal/sandboxagent/services (§14.2) and
// internal/sandboxagent/gitclone (§6.4): all three spawn a real OS
// process via internal/sandboxagent/supervisor.Supervisor.Spawn (never a
// bare exec.Command) and wait for it to become usable before handing
// control back to their caller.
//
// Spawn does exactly three things: (1) allocates a real, currently-unused
// TCP port by binding 127.0.0.1:0 and reading back the kernel-assigned
// port — the same technique internal/sandboxagent/services' own tests
// already use for freePort, promoted here to real (non-test) code since
// sandbox-agent genuinely needs an ephemeral port at boot time, not just
// under test; (2) spawns `opencode serve --port <port> --hostname
// 127.0.0.1` in the sandbox's own workspace directory (so OpenCode's
// directory-relative project/session resolution defaults correctly
// without ever needing an explicit `?directory=` query param on every
// adapter request — see internal/adapters/outbound/opencode's own doc.go);
// (3) polls GET /api/health (bounded by platform.Timeouts.
// OpenCodeReadinessTimeout, retried every
// platform.Timeouts.OpenCodeReadinessPollInterval — both new, §7
// standalone additions to platform/timeouts.go) until it succeeds, the
// process exits first (a crash before ever becoming healthy — a fail-fast,
// bounded error, never a hang), or the readiness timeout expires.
//
// Once healthy, Spawn makes ONE further best-effort GET /global/health
// call to discover OpenCode's own reported version string (§7: "Pin the
// OpenCode version in the image; record it in the boot fingerprint") —
// verified live against the real, installed OpenCode 1.17.15 binary during
// this Step's own research pass: /global/health uniquely returns
// {"healthy":true,"version":"..."} (/api/health, used for the readiness
// poll above, reports only {"healthy":true}, no version field). This
// avoids a second `opencode --version` shell-out — the version is already
// obtainable from the server this Spawn call just started, per §7's own
// guidance. Version discovery is best-effort, exactly like
// internal/sandboxagent/boot.DiscoverRepoSHAs' own "omission is a valid
// outcome, never an error" contract: any failure (network hiccup, an
// OpenCode version that changes this shape) yields "" rather than an
// error, since sandbox-agent's own boot must never block on it.
//
// This package does NOT itself construct internal/adapters/outbound/
// opencode.Adapter or drive its SSE loop — Result.BaseURL is handed to
// opencode.New by the caller (cmd/sandbox-agent/main.go), mirroring the
// EXACT separation internal/adapters/outbound/modal already has from the
// process supervisor's own process concerns: adapters in this codebase
// are I/O-light HTTP clients, process supervision is sandbox-agent's own
// separate concern.
//
// Spawn's own runtimeCredential parameter (TECHNICAL_PLAN.md §30.5) is
// this package's half of the OS-level isolation between sandbox-agent and
// the agent runtime: opencode serve, and every shell/tool it forks on the
// agent's behalf, is THE "agent runtime" that section names as running
// same-UID as sandbox-agent today -- one process-environment read
// (sandbox-agent's own /proc/<pid>/environ) or one on-disk file read (the
// credential cache, 0600 "outside /workspace" but same-UID-readable) away
// from the sandbox bearer and the SCM credential respectively. A non-nil
// runtimeCredential is threaded straight into
// internal/sandboxagent/supervisor.Spec's own Credential field, applied
// to nothing else this package spawns (there is only ever the one
// process). See that field's own doc comment for the exact mechanics
// (kernel-enforced, requires the calling process's own privilege to take
// effect) and internal/sandboxagent/supervisor/credential_test.go /
// spawn_test.go's own TestSpawn_RuntimeCredentialDropsCannotReadAnotherUIDsFile
// for this isolation proven by actual execution, in a rooted Linux
// container.
package opencodeproc
