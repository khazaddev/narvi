// This file (opencodeconfig.go) implements Step 72's own ("sandbox
// secrets & opencode config", §27.2) sandbox-agent-side FETCH + WRITE of
// OpenCode config documents into OpenCode's own documented config slots.
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
// setting the project config (or §8.2's sentinel-fix write, which
// targets that SAME project slot -- opencode/sentinelfixagent.go's own
// doc comment records having verified THAT write path live against the
// real pinned 1.17.15 binary) already sets, confirming the security-
// relevant claim §27.2 makes ("a customer-authored config can never
// override the security-relevant agent restriction") by the engine's own
// documented ordering, not by a Narvi convention.
//
// Adversarial-review CRITICAL fix: levels ABOVE project (.opencode
// directories, inline config OPENCODE_CONFIG_CONTENT, managed files,
// macOS preferences) are where OpenCode's own precedence stops helping --
// a document injected THERE would sit ABOVE the project slot, capable of
// overriding §8.2's own restriction. An EARLIER version of this
// codebase's own comment here claimed "no Narvi mechanism writes to any
// of them" as though that settled the matter -- it did not: nothing
// PREVENTED a maintainer-authored sandbox_secrets row literally named
// "OPENCODE_CONFIG_CONTENT" from being saved and threaded into `opencode
// serve`'s own env by this Step's OWN sibling mechanism
// (sandboxsecrets.go), which would have been indistinguishable, from
// OpenCode's perspective, from Narvi itself injecting at that slot. The
// actual, now-true guarantee is structural, not incidental:
// internal/domain/sandboxsecret.ValidateName reserves the ENTIRE
// OPENCODE_ namespace (OpenCodeReservedPrefix, that package's own name.go)
// -- no sandbox_secrets row can EVER be saved under OPENCODE_CONFIG,
// OPENCODE_CONFIG_CONTENT, or any other OpenCode-owned name, so a
// customer-authored value can never reach any slot this codebase does not
// deliberately choose to write to. openCodeConfigEnvVar (below) is built
// FROM that exact same exported prefix constant specifically so the
// reservation and this file's own injection can never drift apart again.

package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/khazaddev/narvi/internal/domain/sandboxsecret"
	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/internal/sandboxagent/boot"
	"github.com/khazaddev/narvi/internal/sandboxagent/credentials"
)

// openCodeEnvironmentConfigPath is where sandbox-agent writes the
// environment-scoped OpenCode config document -- OUTSIDE the workspace
// (never a repo tree, so never committable), mirroring
// boot.ImageManifestPath's own "/narvi/..." convention (this codebase's
// established sandbox-owned, non-workspace path prefix) and §27.3/§27.4's
// own future "/narvi/identity/" convention for the identical reason.
// OPENCODE_CONFIG is set to this exact path once the file is written (or
// left pointing at whatever ALREADY exists there from a prior boot on a
// failed fetch -- see applyOpenCodeConfig's own doc comment).
const openCodeEnvironmentConfigPath = "/narvi/opencode-environment-config.json"

// openCodeGlobalConfigRelPath is OpenCode's own documented global config
// slot, relative to whatever user's home directory sandbox-agent (and,
// since supervisor.Spawn sets no Credential/UID -- see this codebase's
// own named debt, scmcredentials.go -- opencode serve itself, same UID)
// runs as -- §27.2: "~/.config/opencode/opencode.json".
const openCodeGlobalConfigRelPath = ".config/opencode/opencode.json"

// openCodeConfigEnvVar is OpenCode's own documented env var naming its
// custom config slot (§27.2's own precedence: remote -> global -> custom
// -> project). Built directly from sandboxsecret.OpenCodeReservedPrefix
// (adversarial-review CRITICAL fix, this file's own top doc comment)
// rather than a second, independent "OPENCODE_CONFIG" literal -- so the
// name this file injects and the namespace ValidateName reserves can
// never drift apart: if this literal ever changed, it would automatically
// stay inside the reserved prefix, and TestSandboxSecretValidateName_
// RejectsOpenCodeConfigEnvVar (opencodeconfig_test.go) pins that
// invariant directly.
const openCodeConfigEnvVar = sandboxsecret.OpenCodeReservedPrefix + "CONFIG"

// fetchOpenCodeConfig fetches this session's own global + environment
// OpenCode config documents from CP (POST /sessions/{id}/opencode-config),
// retrying a transport error or 5xx up to
// timeouts.OpenCodeConfigFetchMaxAttempts times (§27.1's own "with bounded
// retry", applied identically here per §27.2's own "delivered at boot...
// same handshake" framing; see cmd/sandbox-agent/deliveryretry.go's own
// classifyDeliveryFetchError for the shared classification) -- mirrors
// fetchSandboxSecrets/fetchProviderCredentials' own identical best-effort,
// never-fatal-to-boot posture.
//
// fetchOK=false means every attempt failed (or the very first one hit a
// terminal 401/403/404/410) -- logged (Warn) and returned alongside the
// zero-value credentials.OpenCodeConfigDelivery. On a FRESH sandbox (no
// prior boot ever wrote either document to disk) this really does mean
// "boot with neither document injected, exactly as if this feature did
// not exist". On a snapshot-restored boot, it does NOT mean that --
// applyOpenCodeConfig's own doc comment explains the "keep last known
// good" decision this Step makes for that case, matching this codebase's
// own established §13.2 precedent (internal/app/identitylink.
// FetchEmailWithRetry: "never null-out ... on transient failure... keep
// the last known value").
//
// fetchOK=true with both fields empty is the overwhelming common case
// (nothing configured for this session) and is NOT a degraded outcome.
func fetchOpenCodeConfig(ctx context.Context, cfg boot.Config, timeouts platform.Timeouts) (delivery credentials.OpenCodeConfigDelivery, fetchOK bool) {
	client, err := credentials.NewCPClient(cfg.SessionConfig.ControlPlaneWsUrl, timeouts.OpenCodeConfigFetchTimeout)
	if err != nil {
		slog.Warn("sandbox-agent: build opencode-config CP client failed, booting with no injected opencode config", "error", err)
		return credentials.OpenCodeConfigDelivery{}, false
	}

	retryErr := platform.Retry(ctx, timeouts.OpenCodeConfigFetchMaxAttempts, timeouts.OpenCodeConfigFetchRetryBaseDelay, timeouts.OpenCodeConfigFetchRetryMaxDelay, func() error {
		fetchCtx, cancel := context.WithTimeout(ctx, timeouts.OpenCodeConfigFetchTimeout)
		defer cancel()

		d, fetchErr := client.FetchOpenCodeConfig(fetchCtx, cfg.SessionConfig.SessionId, cfg.SessionConfig.SandboxToken, cfg.SessionConfig.Gen)
		if fetchErr != nil {
			return classifyDeliveryFetchError(fetchErr)
		}
		delivery = d
		return nil
	})
	if retryErr != nil {
		slog.Warn("sandbox-agent: fetch opencode config exhausted every retry attempt, booting with no injected opencode config", "error", retryErr)
		return credentials.OpenCodeConfigDelivery{}, false
	}

	slog.Info("sandbox-agent: resolved opencode config",
		"global_configured", len(delivery.Global) > 0, "environment_configured", len(delivery.Environment) > 0)
	return delivery, true
}

// applyOpenCodeConfig writes delivery's own documents to OpenCode's
// documented config slots, and returns zero or one already-built
// "NAME=VALUE" entry ("OPENCODE_CONFIG=<environmentConfigPath>") for the
// caller to thread into opencodeproc.Spawn's own sandboxSecretEnv
// parameter -- adversarial-review HIGH fix: an EARLIER version of this
// function instead os.Setenv'd OPENCODE_CONFIG onto sandbox-agent's own
// process, reusing the SAME (now-removed) mechanism sandboxsecrets.go's
// applySandboxSecretEnv used. See opencodeproc.Spawn's own doc comment
// for the full "why threading, never os.Setenv" reasoning -- it applies
// here identically even though OPENCODE_CONFIG itself is never
// attacker-controlled (it is this Step's OWN injected constant path, not
// a resolved secret): the fix is about never mutating sandbox-agent's own
// process environment at all, for ANY of this Step's injected values,
// keeping this whole class of failure (a bad PATH/HOME corrupting the
// SUPERVISOR) unrepresentable regardless of which value would have
// triggered it.
//
// Adversarial-review MEDIUM fix (configuration revocation across a
// snapshot restore): this function is now AUTHORITATIVE for both config
// files, not merely additive -- an EARLIER version only ever WROTE a file
// when its own scope's document was present, and never removed one, so a
// scope an admin had since deleted server-side left its PRIOR document
// sitting on disk forever, silently re-merged by OpenCode on every
// subsequent restored-snapshot boot (these paths live in the sandbox
// filesystem, which a snapshot_restore boot restores verbatim). The fix
// distinguishes three cases per scope, deliberately NOT the same decision
// for all three:
//
//  1. Document present (fetchOK is irrelevant -- CP returned real content
//     for this scope): write it, exactly as before.
//  2. Document ABSENT and fetchOK=true (CP was successfully asked and
//     genuinely has nothing configured for this scope): remove any stale
//     file at that scope's path. This is the CLEARLY RIGHT case -- a
//     successful, authoritative "nothing configured" answer must always
//     win over whatever was on disk from a previous boot, or deleting the
//     admin-removed document server-side would never actually take
//     effect on a restored snapshot. This is the bug this fix closes.
//  3. Document absent and fetchOK=false (the fetch itself failed, even
//     after retrying -- see fetchOpenCodeConfig's own doc comment): a
//     JUDGEMENT CALL, made explicitly here rather than defaulting
//     silently to either of the other two: KEEP whatever is already on
//     disk untouched (neither write nor remove), and for the environment
//     scope specifically, still point OPENCODE_CONFIG at it if it exists.
//     This mirrors this codebase's own established §13.2 precedent
//     (internal/app/identitylink.FetchEmailWithRetry's own doc comment:
//     "never null-out ... on transient failure... retry with backoff and
//     keep the last known value") rather than treating "the fetch failed"
//     the same as "the fetch succeeded and found nothing" -- those are
//     different facts about the world, and only the second one is
//     authoritative. Treating a transient CP blip as "revoke this
//     session's own working config" would make a working session WORSE
//     specifically on the failure path this Step's whole "warn and
//     continue" posture exists to protect against; a stale-but-known-good
//     config is a strictly better outcome than no config at all for a
//     session that had one working moments ago.
//
// Each write/removal is independently best-effort, exactly like before --
// a failure on one document does not prevent attempting the other, and
// every failure is logged (Warn) and otherwise non-fatal.
//
// homeDir/environmentConfigPath are passed in (never resolved/hardcoded
// internally) so this function stays testable without depending on the
// real process's own HOME or writing into the real, root-owned "/narvi"
// -- the production call site (run(), main.go) passes os.UserHomeDir()'s
// own result and the openCodeEnvironmentConfigPath constant.
func applyOpenCodeConfig(delivery credentials.OpenCodeConfigDelivery, fetchOK bool, homeDir, environmentConfigPath string) []string {
	globalPath := filepath.Join(homeDir, openCodeGlobalConfigRelPath)

	switch {
	case len(delivery.Global) > 0:
		writeOpenCodeConfigDoc(delivery.Global, globalPath, "global")
	case fetchOK:
		// Case 2 above: a successful, authoritative "nothing configured"
		// answer -- any stale file from a prior boot must go.
		removeStaleOpenCodeConfigDoc(globalPath, "global")
		// fetchOK == false (case 3): deliberately no branch at all --
		// leave whatever is on disk untouched. There is no env var for
		// the global scope, so there is nothing further to decide here.
	}

	switch {
	case len(delivery.Environment) > 0:
		if writeOpenCodeConfigDoc(delivery.Environment, environmentConfigPath, "environment") {
			return []string{openCodeConfigEnvVar + "=" + environmentConfigPath}
		}
		return nil
	case fetchOK:
		removeStaleOpenCodeConfigDoc(environmentConfigPath, "environment")
		return nil
	default:
		// Case 3 above: keep whatever is on disk from a prior boot
		// (never null-out on transient failure) -- and if a document IS
		// sitting there (a snapshot-restored boot whose own prior fetch
		// once succeeded), still point OPENCODE_CONFIG at it so this
		// boot's `opencode serve` sees the SAME config as last boot. A
		// FRESH sandbox with nothing on disk yet correctly gets no env
		// var at all -- "as if this feature did not exist" is only true
		// in that specific, first-boot case; see fetchOpenCodeConfig's
		// own doc comment.
		if openCodeConfigFileExists(environmentConfigPath) {
			return []string{openCodeConfigEnvVar + "=" + environmentConfigPath}
		}
		return nil
	}
}

// writeOpenCodeConfigDoc writes doc to path (creating its parent
// directory first), returning whether the write fully succeeded. Any
// failure is logged (Warn, scope-labeled) and non-fatal -- matches this
// feature's whole "warn and continue" posture (§27.1, applied identically
// here per §27.2's own framing).
func writeOpenCodeConfigDoc(doc []byte, path, scope string) bool {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		slog.Warn("sandbox-agent: create opencode config dir failed, skipping this document", "scope", scope, "path", filepath.Dir(path), "error", err)
		return false
	}
	if err := os.WriteFile(path, doc, 0o644); err != nil {
		slog.Warn("sandbox-agent: write opencode config failed, skipping this document", "scope", scope, "path", path, "error", err)
		return false
	}
	slog.Info("sandbox-agent: wrote opencode config", "scope", scope, "path", path)
	return true
}

// removeStaleOpenCodeConfigDoc removes path if it exists -- adversarial-
// review MEDIUM fix (configuration revocation), applyOpenCodeConfig's own
// doc comment case 2. Absence is the overwhelming common case (a session
// that has never had a document at this scope, or already had it
// removed) and is silently a no-op, never logged or treated as an error.
// A genuine removal failure (e.g. permission denied) is logged (Warn) and
// non-fatal -- the stale file simply stays, exactly as if this fix did
// not run; a future boot gets another chance.
func removeStaleOpenCodeConfigDoc(path, scope string) {
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return
		}
		slog.Warn("sandbox-agent: remove stale opencode config failed", "scope", scope, "path", path, "error", err)
		return
	}
	slog.Info("sandbox-agent: removed stale opencode config (scope no longer configured)", "scope", scope, "path", path)
}

// openCodeConfigFileExists reports whether path exists on disk -- used
// only by applyOpenCodeConfig's own fetchOK=false branch (case 3) to
// decide whether a prior boot's environment document is still there to
// point OPENCODE_CONFIG at.
func openCodeConfigFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
