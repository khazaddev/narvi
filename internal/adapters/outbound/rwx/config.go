package rwx

import "github.com/khazaddev/narvi/internal/platform"

// Config configures a Provider (New) -- the SandboxProvider half of this
// package's two transports (doc.go): the pinned `rwx` CLI, shelled out as
// a subprocess for sandbox lifecycle (start|stop|list; §4.1.1). Every
// field is sourced from the caller's own configuration -- New never
// hardcodes a binary path or token, mirroring modal.Config's own identical
// convention.
type Config struct {
	// CLIPath is the pinned `rwx` binary's path (or a bare name resolved
	// via PATH) -- §4.1.1: "pin the version, record it in the boot
	// fingerprint's environment" (recording the fingerprint itself is a
	// sandbox-agent-side concern, internal/sandboxagent/boot, outside this
	// adapter's own boundary). Required -- New returns MissingConfigError
	// otherwise.
	CLIPath string

	// AccessToken is RWX's own documented credential ("For programmatic
	// use, set the token as the RWX_ACCESS_TOKEN environment variable").
	// Passed to every CLI subprocess via its environment, NEVER as argv
	// (§5.2's leak-class discipline -- argv is visible to process
	// listings; matching the safety property modal.Provider's own
	// Authorization-header threading already gets for free from using
	// HTTP). Should be an RWX SERVICE ACCOUNT token per RWX's own guidance
	// (a personal token "acts as you"; a service account "is owned by the
	// organization, so it survives you leaving it") -- this package does
	// not itself distinguish the two token shapes, it only threads
	// whatever value is configured through unchanged, exactly like
	// modal.Config.AuthToken. Required -- New returns MissingConfigError
	// otherwise.
	AccessToken string

	// Timeouts supplies platform.Timeouts.RWXCLIExecTimeout (bounds every
	// subprocess invocation below) and platform.Timeouts.
	// RWXSandboxInactivityTimeout (CreateSandbox's own `--inactivity-timeout`
	// value, §4.1.1: "set above Narvi's own session-idle authority").
	Timeouts platform.Timeouts
}
