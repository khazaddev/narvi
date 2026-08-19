// This file (substrate.go) implements the ONE decision §27.5/§27.6's
// "fail-closed, twice" rule shares between its two independent call sites
// (Step 74 brief, point A): refused up-front at session-creation time
// (internal/adapters/inbound/httpapi.CreateSessionCore, before any
// Postgres write) when the configured provider reports no support, and
// re-checked again at dispatch time (internal/app/sessionactor.
// tryPlanSpawn, immediately after reading the live ports.Capabilities)
// before a spawn/restore/resume actually proceeds. Both call sites invoke
// CheckSubstrateCapabilities independently -- see each call site's own
// doc comment for why a single check at either end alone would leave a
// real hole (a provider swapped out between session creation and a later
// respawn; an Environment's docker/egress requirement edited after the
// session already exists).
//
// Pure: takes plain, already-extracted capability bools rather than
// importing internal/app/ports.Capabilities directly, mirroring domain/
// sandbox.EvaluateSpawnDecision's own supportsPersistentResume parameter
// -- this keeps internal/domain free of any dependency on the ports
// layer (CLAUDE.md's "no I/O in /internal/domain" extends naturally to
// "no port interfaces either": a port is how I/O reaches this system, and
// this package must stay reasoning-only regardless of which adapter is
// live).
package environment

import "errors"

// ErrDockerUnsupported means an Environment's own DockerRequired is true,
// but the configured SandboxProvider does not report DockerInSandbox
// support (§27.5).
var ErrDockerUnsupported = errors.New("environment: this Environment requires docker-in-sandbox, but the configured sandbox provider does not support it")

// ErrEgressEnforcementUnsupported means an Environment's own EgressPolicy
// requires enforcement (EgressPolicy.RequiresEnforcement(), i.e. Mode ==
// EgressModeAllowlist), but the configured SandboxProvider does not
// report EgressPolicy support (§27.6).
var ErrEgressEnforcementUnsupported = errors.New("environment: this Environment requires enforced egress-policy allowlist mode, but the configured sandbox provider does not support enforcing it")

// CheckSubstrateCapabilities reports the first requirement dockerRequired/
// egressEnforcementRequired name that providerSupportsDocker/
// providerSupportsEgressPolicy cannot honor, or nil when every requirement
// is supported (including the common case where neither is requested at
// all, regardless of what the provider supports). Docker is checked
// before egress purely for a deterministic, single-error return when both
// happen to fail at once -- callers needing every violation, not just the
// first, may call this twice with one requirement zeroed each time.
func CheckSubstrateCapabilities(dockerRequired, egressEnforcementRequired, providerSupportsDocker, providerSupportsEgressPolicy bool) error {
	if dockerRequired && !providerSupportsDocker {
		return ErrDockerUnsupported
	}
	if egressEnforcementRequired && !providerSupportsEgressPolicy {
		return ErrEgressEnforcementUnsupported
	}
	return nil
}
