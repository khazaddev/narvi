// This file (sandboxsecrets.go) implements Step 72's own ("sandbox
// secrets & opencode config", §27.1) sandbox-agent-side FETCH +
// INJECTION of general sandbox_secrets, mirroring main.go's own
// fetchProviderCredentials/providerCredentialSpawnEnv split (Step 53)
// exactly in spirit, adapted to this feature's own "thread into EVERY
// spawned process" requirement rather than opencode serve alone.
//
// # Injection mechanism: sandbox-agent's own process environment, not a
// threaded parameter
//
// §27.1 says sandbox-agent must thread the resolved map into hooks (via
// runRepoHooks' own EXISTING EnvWithout seam, internal/sandboxagent/boot/
// hooks.go), services.yml services, and opencode serve -- three call
// sites, none of which this Step changes the SIGNATURE of. Every one of
// them already builds its own child env by calling
// supervisor.EnvWithout(names...), which reads the CURRENT PROCESS's own
// os.Environ() fresh, every time it's called (see that function's own
// doc comment: "a caller uses this when a child has confirmed no
// legitimate need for one or more specific things sandbox-agent's own
// process environment happens to carry"). applySandboxSecretEnv (below)
// exploits exactly that: it os.Setenv's every resolved secret onto
// sandbox-agent's OWN process environment, once, before ANY of the three
// call sites above ever runs (run(), below, does so immediately after
// fetchSandboxSecrets, ahead of opencodeproc.Spawn AND
// runBootSequence/RunHooks/services.Run) -- so every SUBSEQUENT
// EnvWithout call anywhere in this binary automatically inherits them,
// with ZERO changes to boot/hooks.go, boot/runboot.go, services/run.go,
// or opencodeproc/spawn.go. This is not a workaround; it is the exact
// seam EnvWithout's own doc comment already describes, used for its
// intended purpose. Setting these onto sandbox-agent's own process is not
// a NEW exposure either -- see internal/domain/sandboxsecret's own doc.go
// for why "in-sandbox secrecy from the agent" is already a stated
// non-goal, independent of this Step's own injection mechanism (there is
// no privilege boundary between the agent and sandbox-agent as of this
// Step -- see that same doc comment).
//
// Every name in a resolved map was ALREADY validated at CRUD write time
// (internal/domain/sandboxsecret.ValidateName, enforced server-side) --
// this file trusts that and performs no re-validation before os.Setenv;
// re-validating a value CP itself already accepted would just be a
// second, redundant copy of the same rule with its own drift risk.
//
// # Structurally never reaches BuildImage
//
// This whole mechanism runs ONLY inside the `if cfg.SessionConfig != nil`
// branch of run() (below) -- and a real ports.SandboxProvider.BuildImage
// call NEVER produces a sandbox with a SessionConfig at all: ImageSpec
// (internal/app/ports/createspec.go) carries {Base, Repos, RuntimeVersion,
// CacheMount} and has no field a provider could even use to construct one
// (see that type's own pinning test, TestImageSpec_HasNoSecretCarryingField,
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
	"os"
	"sort"
	"time"

	"github.com/khazaddev/narvi/internal/sandboxagent/boot"
	"github.com/khazaddev/narvi/internal/sandboxagent/credentials"
)

// fetchSandboxSecrets fetches this session's own resolved sandbox secrets
// from CP (POST /sessions/{id}/sandbox-secrets) -- mirrors
// fetchProviderCredentials' own identical best-effort, never-fatal-to-
// boot posture (§27.1: "warn and continue... never a boot failure"). A
// failure here is logged (Warn, never Error) and returns nil -- the
// caller then simply boots with today's unchanged, ambient environment,
// exactly as if this feature did not exist.
//
// Never logs any resolved secret VALUE, at any point -- only names (never
// secret material) for observability, matching
// fetchProviderCredentials' own identical discipline.
func fetchSandboxSecrets(ctx context.Context, cfg boot.Config, timeout time.Duration) map[string]string {
	client, err := credentials.NewCPClient(cfg.SessionConfig.ControlPlaneWsUrl, timeout)
	if err != nil {
		slog.Warn("sandbox-agent: build sandbox-secrets CP client failed, booting with no resolved sandbox secret", "error", err)
		return nil
	}

	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resolved, err := client.FetchSandboxSecrets(fetchCtx, cfg.SessionConfig.SessionId, cfg.SessionConfig.SandboxToken, cfg.SessionConfig.Gen)
	if err != nil {
		slog.Warn("sandbox-agent: fetch sandbox secrets failed, booting with no resolved sandbox secret", "error", err)
		return nil
	}

	if len(resolved) > 0 {
		names := make([]string, 0, len(resolved))
		for name := range resolved {
			names = append(names, name)
		}
		sort.Strings(names)
		slog.Info("sandbox-agent: resolved sandbox secrets", "names", names)
	}
	return resolved
}

// applySandboxSecretEnv os.Setenv's every entry in secrets onto
// sandbox-agent's OWN process environment -- see this file's own top doc
// comment for the full "why" (the EnvWithout seam every hook/service/
// opencode-serve spawn already reads from). Returns the names actually
// set (sorted, for logging only) -- never the values. A nil/empty
// secrets map is a correct, silent no-op (the overwhelming common case:
// nothing configured for this session).
func applySandboxSecretEnv(secrets map[string]string) []string {
	if len(secrets) == 0 {
		return nil
	}
	names := make([]string, 0, len(secrets))
	for name, value := range secrets {
		if err := os.Setenv(name, value); err != nil {
			// os.Setenv fails only on a malformed name (e.g. containing
			// '=' or a NUL byte) -- structurally unreachable for a name
			// that already passed internal/domain/sandboxsecret.
			// ValidateName's POSIX-shape check at CRUD write time, but
			// defended against anyway rather than silently dropping a
			// secret an operator believes is configured. Logged and
			// skipped -- never fatal to boot, matching this whole
			// mechanism's own "warn and continue" posture.
			slog.Warn("sandbox-agent: set sandbox secret env var failed, skipping this one secret", "name", name, "error", err)
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
