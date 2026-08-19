// This file (opencodeconfig.go) implements Step 72's own ("sandbox
// secrets & opencode config", §27.2) sandbox-agent-side FETCH + WRITE of
// OpenCode config documents into OpenCode's own documented config slots
// -- mirrors sandboxsecrets.go's own fetch/apply split, and reuses that
// SAME file's "os.Setenv onto sandbox-agent's own process, ahead of every
// EnvWithout call" mechanism for OPENCODE_CONFIG (the environment
// document's own slot) rather than inventing a second injection
// mechanism for this one extra env var.
//
// # Verified against OpenCode's own real precedence, not merely assumed
//
// §27.2's own design claim is that occupying OpenCode's global + custom
// slots (rather than its project slot) keeps a repo's own committed
// opencode.json AND Step 48's sentinel-fix capability-restriction write
// (opencode/sentinelfixagent.go, which targets the WORKSPACE opencode.json
// -- OpenCode's "project" slot) structurally above anything this Step
// injects. CONFIRMED against OpenCode's own current public documentation
// (opencode.ai/docs/config, fetched during this Step): "Configuration
// files are merged together, not replaced... Later configs override
// earlier ones only for conflicting keys", with the real precedence order,
// LOWEST to HIGHEST: remote config -> global config
// (~/.config/opencode/opencode.json) -> custom config (OPENCODE_CONFIG
// env var) -> PROJECT config (opencode.json in the project/workspace
// root) -> .opencode directories -> inline config (OPENCODE_CONFIG_CONTENT
// env var) -> managed config files -> macOS managed preferences. This
// matches §27.2's own cited order exactly for the 4 levels it names
// (remote < global < custom < project, later-wins) -- global and custom
// (this file's own two injection points) sit BELOW project, so a
// customer-authored config injected at either level can never override a
// setting the project config (or Step 48's sentinel-fix write, which
// targets that SAME project slot -- opencode/sentinelfixagent.go's own
// doc comment records having verified THAT write path live against the
// real pinned 1.17.15 binary) already sets, confirming the security-
// relevant claim §27.2 makes ("a customer-authored config can never
// override the security-relevant agent restriction") by the engine's own
// documented ordering, not by a Narvi convention. Levels ABOVE project
// (.opencode directories, inline config, managed files, macOS
// preferences) are irrelevant here -- no Narvi mechanism writes to any of
// them.

package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/khazaddev/narvi/internal/sandboxagent/boot"
	"github.com/khazaddev/narvi/internal/sandboxagent/credentials"
)

// openCodeEnvironmentConfigPath is where sandbox-agent writes the
// environment-scoped OpenCode config document -- OUTSIDE the workspace
// (never a repo tree, so never committable), mirroring
// boot.ImageManifestPath's own "/narvi/..." convention (this codebase's
// established sandbox-owned, non-workspace path prefix) and §27.3/§27.4's
// own future "/narvi/identity/" convention for the identical reason.
// OPENCODE_CONFIG is set to this exact path once the file is written.
const openCodeEnvironmentConfigPath = "/narvi/opencode-environment-config.json"

// openCodeGlobalConfigRelPath is OpenCode's own documented global config
// slot, relative to whatever user's home directory sandbox-agent (and,
// since supervisor.Spawn sets no Credential/UID -- see this codebase's
// own named debt, scmcredentials.go -- opencode serve itself, same UID)
// runs as -- §27.2: "~/.config/opencode/opencode.json".
const openCodeGlobalConfigRelPath = ".config/opencode/opencode.json"

// openCodeConfigEnvVar is OpenCode's own documented env var naming its
// custom config slot (§27.2's own precedence: remote -> global -> custom
// -> project) -- the literal name lives here, once, rather than repeated
// at every call site.
const openCodeConfigEnvVar = "OPENCODE_CONFIG"

// fetchOpenCodeConfig fetches this session's own global + environment
// OpenCode config documents from CP (POST /sessions/{id}/opencode-config)
// -- mirrors fetchSandboxSecrets/fetchProviderCredentials' own identical
// best-effort, never-fatal-to-boot posture. A failure here is logged
// (Warn) and returns a zero-value credentials.OpenCodeConfigDelivery
// (both fields nil) -- the caller then simply boots with neither document
// injected, exactly as if this feature did not exist.
func fetchOpenCodeConfig(ctx context.Context, cfg boot.Config, timeout time.Duration) credentials.OpenCodeConfigDelivery {
	client, err := credentials.NewCPClient(cfg.SessionConfig.ControlPlaneWsUrl, timeout)
	if err != nil {
		slog.Warn("sandbox-agent: build opencode-config CP client failed, booting with no injected opencode config", "error", err)
		return credentials.OpenCodeConfigDelivery{}
	}

	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	delivery, err := client.FetchOpenCodeConfig(fetchCtx, cfg.SessionConfig.SessionId, cfg.SessionConfig.SandboxToken, cfg.SessionConfig.Gen)
	if err != nil {
		slog.Warn("sandbox-agent: fetch opencode config failed, booting with no injected opencode config", "error", err)
		return credentials.OpenCodeConfigDelivery{}
	}

	slog.Info("sandbox-agent: resolved opencode config",
		"global_configured", len(delivery.Global) > 0, "environment_configured", len(delivery.Environment) > 0)
	return delivery
}

// applyOpenCodeConfig writes delivery's own documents to OpenCode's
// documented config slots and sets OPENCODE_CONFIG on sandbox-agent's own
// process environment for the environment document -- see
// sandboxsecrets.go's own top doc comment for why os.Setenv (not a
// threaded parameter) is this Step's own injection mechanism, reused here
// for the SAME reason: OPENCODE_CONFIG only matters to `opencode serve`,
// but setting it process-wide via the SAME EnvWithout seam is harmless
// for hooks/services (they simply never read it), and keeps this Step to
// exactly ONE injection mechanism rather than two.
//
// Each write is independently best-effort: a failure on one document
// (the global write, say) does not prevent attempting the other. Every
// failure is logged (Warn) and otherwise non-fatal -- matching this
// feature's whole "warn and continue" posture (§27.1, applied identically
// here per §27.2's own "delivered at boot... same handshake" framing).
// homeDir/environmentConfigPath are passed in (never resolved/hardcoded
// internally) so this function stays testable without depending on the
// real process's own HOME or writing into the real, root-owned
// "/narvi" -- the production call site (run(), below) passes
// os.UserHomeDir()'s own result and the openCodeEnvironmentConfigPath
// constant.
func applyOpenCodeConfig(delivery credentials.OpenCodeConfigDelivery, homeDir, environmentConfigPath string) {
	if len(delivery.Global) > 0 {
		path := filepath.Join(homeDir, openCodeGlobalConfigRelPath)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			slog.Warn("sandbox-agent: create opencode global config dir failed, skipping global opencode config", "path", filepath.Dir(path), "error", err)
		} else if err := os.WriteFile(path, delivery.Global, 0o644); err != nil {
			slog.Warn("sandbox-agent: write opencode global config failed, skipping global opencode config", "path", path, "error", err)
		} else {
			slog.Info("sandbox-agent: wrote opencode global config", "path", path)
		}
	}

	if len(delivery.Environment) > 0 {
		if err := os.MkdirAll(filepath.Dir(environmentConfigPath), 0o755); err != nil {
			slog.Warn("sandbox-agent: create opencode environment config dir failed, skipping environment opencode config", "path", filepath.Dir(environmentConfigPath), "error", err)
		} else if err := os.WriteFile(environmentConfigPath, delivery.Environment, 0o644); err != nil {
			slog.Warn("sandbox-agent: write opencode environment config failed, skipping environment opencode config", "path", environmentConfigPath, "error", err)
		} else if err := os.Setenv(openCodeConfigEnvVar, environmentConfigPath); err != nil {
			slog.Warn("sandbox-agent: set OPENCODE_CONFIG env var failed, skipping environment opencode config", "error", err)
		} else {
			slog.Info("sandbox-agent: wrote opencode environment config and set OPENCODE_CONFIG", "path", environmentConfigPath)
		}
	}
}
