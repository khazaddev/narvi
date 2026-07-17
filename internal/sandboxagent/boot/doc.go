// Package boot implements sandbox-agent's own boot-sequence orchestration
// (§6.4, §5.3, §14.2): reading and validating process configuration
// (config.go), assembling the boot fingerprint §5.3 requires be logged
// first (fingerprint.go), running the hook policy from
// internal/domain/sandboxboot against real, disk-resident scripts using
// internal/sandboxagent/supervisor for execution (hooks.go), and -- as of
// Step 14 -- RunBoot (runboot.go), the top-level per-repo dispatcher that
// chooses, per repo, between the multi-service manifest contract
// (internal/sandboxagent/services, §14.2) and this package's own
// setup.sh/start.sh hook contract (§6.4), falling back to the latter
// whenever a repo has no .narvi/services.yml. RunBoot (like RunHooks
// before it) takes its repo list as a plain []RepoInfo parameter; as of
// Step 15, main.go populates it from cmd/sandbox-agent's own
// internal/sandboxagent/gitclone.CloneAll results whenever Config.
// SessionConfig is present, and passes nil (today's original no-op)
// otherwise. Step 16/17 are expected to extend this package (or add
// sibling ones) further as the boot sequence grows.
//
// Step 15 also closes the "SESSION_CONFIG delivery" gap Steps 13/14
// repeatedly flagged as honest, undecided territory: config.go's Load()
// now additionally reads an OPTIONAL NARVI_SESSION_CONFIG env var carrying
// the full SESSION_CONFIG document as JSON, parsed into Config.
// SessionConfig (nil when the env var is absent -- a fully valid, correct
// state; dev/CI environments have no live session). When present, its own
// bootMode field is cross-checked against the separately-read
// NARVI_BOOT_MODE, a fail-fast *ModeMismatchError on disagreement --
// the same reconciliation shape as ports.CreateSpec.Validate's
// GenMismatchError. Load() also reads an optional
// NARVI_CREDENTIAL_CACHE_DIR (default /tmp/narvi-credentials, deliberately
// outside WorkspaceDir) consumed by the sibling
// internal/sandboxagent/credentials package's on-disk credential cache.
//
// This package is impure (env vars, disk stat calls, `git rev-parse`
// subprocesses, process supervision) and follows the same fail-fast,
// named-error convention as internal/platform/config.go -- but is a
// deliberately separate, small implementation: sandbox-agent's env vars
// are entirely disjoint from control-plane's own, and platform/config.go
// itself is out of scope for this Step.
package boot
