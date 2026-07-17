// Package boot implements sandbox-agent's own boot-sequence orchestration
// (§6.4, §5.3): reading and validating process configuration (config.go),
// assembling the boot fingerprint §5.3 requires be logged first
// (fingerprint.go), and running the hook policy from
// internal/domain/sandboxboot against real, disk-resident scripts using
// internal/sandboxagent/supervisor for execution (hooks.go). Step 15/16/17
// are expected to extend this package (or add sibling ones) as the boot
// sequence grows; this Step only covers what it actually has inputs for --
// notably, RunHooks takes its repo list as a plain []RepoInfo parameter,
// and main.go wires it with an empty slice today (a real, intentional
// no-op until Step 15 populates it from SESSION_CONFIG).
//
// This package is impure (env vars, disk stat calls, `git rev-parse`
// subprocesses, process supervision) and follows the same fail-fast,
// named-error convention as internal/platform/config.go -- but is a
// deliberately separate, small implementation: sandbox-agent's env vars
// are entirely disjoint from control-plane's own, and platform/config.go
// itself is out of scope for this Step.
package boot
