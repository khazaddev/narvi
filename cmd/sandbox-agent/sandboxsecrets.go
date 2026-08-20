// This file (sandboxsecrets.go) implements §27.1's own ("sandbox
// secrets & opencode config", §27.1) sandbox-agent-side FETCH +
// INJECTION-ENV-BUILDING of general sandbox_secrets, mirroring main.go's
// own fetchProviderCredentials/providerCredentialSpawnEnv split (§25.1)
// exactly in spirit, adapted to this feature's own "thread into EVERY
// spawned process" requirement rather than opencode serve alone.
//
// # Injection mechanism: threaded parameters, NEVER sandbox-agent's own
// process environment
//
// §27.1 says sandbox-agent must thread the resolved map into hooks (via
// runRepoHooks' own EnvWithout seam, internal/sandboxagent/boot/hooks.go),
// services.yml services, and opencode serve -- three call sites.
// sandboxSecretSpawnEnv (below) turns the resolved map into a plain
// []string of already-built "NAME=VALUE" entries -- run() (cmd/sandbox-
// agent/main.go) then passes that SAME slice explicitly into
// boot.RunBoot's own secretEnv parameter (which threads it on into
// runRepoHooks/services.Run) and opencodeproc.Spawn's own sandboxSecretEnv
// parameter. Nothing in this Step calls os.Setenv on sandbox-agent's OWN
// process at any point.
//
// Adversarial-review HIGH fix: an EARLIER version of this file instead
// os.Setenv'd every resolved secret onto sandbox-agent's own process
// environment, exploiting supervisor.EnvWithout's own "subtractive filter
// over os.Environ() at call time" behavior so every LATER EnvWithout call
// anywhere in this binary would pick it up automatically. That mechanism
// had a real, structural flaw §27.1 itself does not license: a resolved
// secret literally named "PATH" corrupts exec.Command's own bare-name
// LookPath resolution for THIS BINARY's own subsequent spawns (Go's
// exec.LookPath resolves against the CALLING process's os.Getenv("PATH")
// at exec.Command() call time -- never from a later cmd.Env -- so this
// affected opencodeproc.Spawn's own "opencode" lookup and gitclone's own
// bare "git" lookups alike), and a secret named "HOME" silently redirected
// os.UserHomeDir() (main.go's own global-OpenCode-config-document write
// path) -- turning what this feature's own explicit "warn and continue,
// never a boot failure" posture (§27.1/§10-P2) requires to be a harmless
// per-secret misconfiguration into a HARD BOOT FAILURE. Threading (this
// file's current form) makes that entire class of failure structurally
// unrepresentable: sandbox-agent's own process environment is never
// touched by any resolved secret, no matter what name a customer chose
// for it -- see opencodeproc.Spawn's own doc comment for the full "why"
// from the injection-target side.
//
// Every name in a resolved map was ALREADY validated at CRUD write time
// (internal/domain/sandboxsecret.ValidateName, enforced server-side by
// internal/adapters/inbound/httpapi/sandboxsecrets.go) -- but
// fetchSandboxSecrets (below) does NOT simply trust that and skip ahead:
// it re-runs the SAME ValidateName against every DELIVERED name, a second
// time, at the point of injection, dropping (never failing the boot on)
// any name that no longer passes. This defense-in-depth re-validation was
// added during §27.1's own review round specifically because the write
// path and the injection path can drift apart -- a control plane rolled
// back to a build predating a later reservation, or a row written by some
// other path entirely, would otherwise still inject a name the CURRENT
// binary's own ValidateName would refuse. §27.4 later widened
// ValidateName itself with cloudidentity.ReservedEnvVarNames/
// clusterbinding.ReservedEnvVarNames (internal/domain/sandboxsecret/
// name.go) -- this file's re-validation call picks up that widened rule
// automatically, with no edit of its own required. See
// fetchSandboxSecrets' own "Defense in depth" comment, below, for the
// full reasoning.
//
// # Bounded retry (§27.1, adversarial-review MEDIUM fix)
//
// §27.1 states this fetch happens "with bounded retry" -- an earlier
// version of this file made exactly one attempt, contradicting that
// explicitly. fetchSandboxSecrets now wraps the fetch in platform.Retry,
// retrying a transport-level failure or a 5xx CP response, but NEVER a
// 401/403/404/410 -- those four are this delivery endpoint's own terminal
// handshake fences (mirroring providercredentialsdelivery.go's identical
// four-way handshake: malformed/absent bearer, gen mismatch, unknown
// session, dead sandbox), and retrying them can never produce a different
// outcome -- this session's own identity/generation is what's wrong, not a
// transient blip, so retrying only adds load for zero chance of success.
// See deliveryretry.go's own classifyDeliveryFetchError for the shared
// classification both this function and fetchOpenCodeConfig
// (opencodeconfig.go) use.
//
// # Structurally never reaches BuildImage
//
// This whole mechanism runs ONLY inside the `if cfg.SessionConfig != nil`
// branch of run() (cmd/sandbox-agent/main.go) -- and a real ports.
// SandboxProvider.BuildImage call NEVER produces a sandbox with a
// SessionConfig at all: ImageSpec (internal/app/ports/createspec.go)
// carries {Base, Repos, RuntimeVersion, CacheMount} and has no field a
// provider could even use to construct one (see that type's own pinning
// test, TestImageSpec_HasNoSecretCarryingField,
// internal/app/ports/createspec_test.go), and the Modal adapter's own
// BuildImage request body (internal/adapters/outbound/modal/provider.go)
// carries no SessionConfig either -- so a build-mode boot's own
// boot.Config.SessionConfig is nil (loadSessionConfig, internal/
// sandboxagent/boot/config.go, returns nil when NARVI_SESSION_CONFIG is
// simply never set, which is exactly a real image build's own case).
// fetchSandboxSecrets/fetchOpenCodeConfig are therefore NEVER CALLED
// during an image build -- not merely "not called in practice", but
// structurally unreachable: there is no sandbox bearer token, session id,
// or CP base URL for either fetch to even construct a request from. §19.8
// rule (a) ("boot-time injection only, never passed to BuildImage") holds
// by construction, not by convention.

package main

import (
	"context"
	"log/slog"
	"sort"

	"github.com/khazaddev/narvi/internal/domain/sandboxsecret"
	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/internal/sandboxagent/boot"
	"github.com/khazaddev/narvi/internal/sandboxagent/credentials"
)

// fetchSandboxSecrets fetches this session's own resolved sandbox secrets
// from CP (POST /sessions/{id}/sandbox-secrets), retrying a transport
// error or 5xx up to timeouts.SandboxSecretFetchMaxAttempts times (§27.1:
// "with bounded retry"; see this file's own top doc comment for the full
// retry-classification reasoning) -- mirrors fetchProviderCredentials' own
// identical best-effort, never-fatal-to-boot posture (§27.1: "warn and
// continue... never a boot failure"). Every attempt failing (or a
// terminal 401/403/404/410 on the very first one) is logged (Warn, never
// Error) and reported via fetchOK=false -- the caller then simply boots
// with today's unchanged, ambient environment, exactly as if this feature
// did not exist, and MAY choose to record that degradation somewhere an
// agent can see it (run(), main.go, folds fetchOK into the AGENTS.md
// degrade-notice list, §27.1's own "recorded in the boot log and
// AGENTS.md" requirement).
//
// fetchOK=true with an EMPTY resolved map is the overwhelming common case
// (nothing configured for this session) and is NOT a degraded outcome --
// callers must not conflate the two; that is exactly why this returns a
// separate bool rather than overloading a nil map to mean "failed".
//
// Never logs any resolved secret VALUE, at any point -- only names (never
// secret material) for observability, matching
// fetchProviderCredentials' own identical discipline.
func fetchSandboxSecrets(ctx context.Context, cfg boot.Config, timeouts platform.Timeouts) (resolved map[string]string, fetchOK bool) {
	client, err := credentials.NewCPClient(cfg.SessionConfig.ControlPlaneWsUrl, timeouts.SandboxSecretFetchTimeout)
	if err != nil {
		slog.Warn("sandbox-agent: build sandbox-secrets CP client failed, booting with no resolved sandbox secret", "error", err)
		return nil, false
	}

	retryErr := platform.Retry(ctx, timeouts.SandboxSecretFetchMaxAttempts, timeouts.SandboxSecretFetchRetryBaseDelay, timeouts.SandboxSecretFetchRetryMaxDelay, func() error {
		fetchCtx, cancel := context.WithTimeout(ctx, timeouts.SandboxSecretFetchTimeout)
		defer cancel()

		r, fetchErr := client.FetchSandboxSecrets(fetchCtx, cfg.SessionConfig.SessionId, cfg.SessionConfig.SandboxToken, cfg.SessionConfig.Gen)
		if fetchErr != nil {
			return classifyDeliveryFetchError(fetchErr)
		}
		resolved = r
		return nil
	})
	if retryErr != nil {
		slog.Warn("sandbox-agent: fetch sandbox secrets exhausted every retry attempt, booting with no resolved sandbox secret", "error", retryErr)
		return nil, false
	}

	// Defense in depth: re-validate every DELIVERED name against the same
	// sandboxsecret.ValidateName the control plane's own write path
	// (internal/adapters/inbound/httpapi/sandboxsecrets.go) already
	// enforces. The reserved namespaces exist to stop a sandbox secret from
	// ever shadowing a mechanism sandbox-agent itself depends on -- most
	// sharply OPENCODE_*, whose inline-config slot OUTRANKS the capability
	// restriction §8.2 writes into the project slot. Enforcing that at
	// the write path alone would make it a rule every future writer has to
	// remember, which §30 already ruled is not a guard; enforcing it again
	// here, at the point of injection, makes the shadowing unrepresentable
	// however a row reached the table -- a second write path added by a
	// later Step, a hand-run INSERT, or a control plane rolled back to a
	// build predating the reservation. Deleting during range is defined
	// behaviour in Go: an entry deleted before it is reached is simply not
	// produced. Drops the offending entry and continues, never fails the
	// boot -- this feature's own degrade policy throughout.
	for name := range resolved {
		if err := sandboxsecret.ValidateName(name); err != nil {
			slog.Warn("sandbox-agent: dropping delivered sandbox secret whose name is not injectable", "name", name, "error", err)
			delete(resolved, name)
		}
	}

	if len(resolved) > 0 {
		names := make([]string, 0, len(resolved))
		for name := range resolved {
			names = append(names, name)
		}
		sort.Strings(names)
		slog.Info("sandbox-agent: resolved sandbox secrets", "names", names)
	}
	return resolved, true
}

// sandboxSecretSpawnEnv maps every entry in secrets onto its own
// already-built "NAME=VALUE" string, ready to thread into
// boot.RunBoot's/opencodeproc.Spawn's own env parameters -- mirrors
// providerCredentialSpawnEnv's own identical "resolved map -> []string of
// spawn-ready entries" shape (main.go). Sorted by name purely for
// deterministic output (a test/log convenience -- exec.Cmd's own Env
// semantics never depend on slice order for a set of UNIQUE keys, which a
// Go map's own keys always are). A nil/empty secrets map is a correct,
// nil return -- the overwhelming common case: nothing configured for this
// session.
func sandboxSecretSpawnEnv(secrets map[string]string) []string {
	if len(secrets) == 0 {
		return nil
	}
	names := make([]string, 0, len(secrets))
	for name := range secrets {
		names = append(names, name)
	}
	sort.Strings(names)

	env := make([]string, 0, len(names))
	for _, name := range names {
		env = append(env, name+"="+secrets[name])
	}
	return env
}
