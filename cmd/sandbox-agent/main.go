// Command sandbox-agent is the static binary shipped into sandbox images
// (§1). Step 13 gave it its first real behavior: boot-mode/hook-policy
// decisions (internal/domain/sandboxboot) plus native process supervision
// (internal/sandboxagent/supervisor) -- process groups, killpg-style
// signaling, reaping, and bounded graceful-then-forceful shutdown. Step 14
// extends its boot dispatch to also supervise a per-repo .narvi/
// services.yml multi-service manifest (internal/sandboxagent/services,
// §14.2) when one is present, falling back to the original setup.sh/
// start.sh hook contract otherwise -- both orchestrated by
// internal/sandboxagent/boot.RunBoot. It logs a boot fingerprint first
// (§5.3), runs the boot sequence for whatever repo list names, then blocks
// until told to shut down.
//
// Step 15 adds two things: (1) when Config.SessionConfig is present (the
// NARVI_SESSION_CONFIG env var was set), run() clones every repo it names
// (internal/sandboxagent/gitclone.CloneAll) and writes the generated
// AGENTS.md manifest BEFORE handing the successfully-cloned subset to
// boot.RunBoot as its []boot.RepoInfo -- when SessionConfig is nil (the
// common dev/test case), repos stays nil exactly as before this Step; (2)
// a SEPARATE "credential-helper" subcommand (main's own dispatch, mirroring
// cmd/control-plane/main.go's own subcommand pattern) that implements
// git's credential-helper protocol end to end (internal/sandboxagent/
// credentials) -- this is the exact command gitclone configures every
// `git clone` to invoke via `-c credential.helper=!'<this binary>'
// credential-helper` (§5.2).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/internal/sandboxagent/boot"
	"github.com/khazaddev/narvi/internal/sandboxagent/credentials"
	"github.com/khazaddev/narvi/internal/sandboxagent/gitclone"
	"github.com/khazaddev/narvi/internal/sandboxagent/services"
	"github.com/khazaddev/narvi/internal/sandboxagent/supervisor"
)

func main() {
	// A bare-bones dispatch, not a flag-parsing library, mirroring
	// cmd/control-plane/main.go's own subcommand pattern: exactly one
	// alternate subcommand exists today ("credential-helper", the process
	// git itself invokes per gitclone's own `-c credential.helper=...`
	// configuration) -- everything else falls through to the normal boot
	// sequence.
	if len(os.Args) >= 2 && os.Args[1] == "credential-helper" {
		if err := runCredentialHelper(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runCredentialHelper implements the "credential-helper <get|store|erase>"
// subcommand git itself invokes (via gitclone's own `-c
// credential.helper=!'<this binary>' credential-helper` configuration on
// every clone). This is a SEPARATE process git spawns, inheriting
// sandbox-agent's own environment (supervisor.Spec.Env is nil/inherit in
// gitclone's Spawn call) -- so re-reading the same NARVI_* env vars here
// via boot.Load() is correct and sufficient, not a duplication of that
// config-loading logic.
func runCredentialHelper(args []string) error {
	if len(args) != 1 {
		return errors.New("sandbox-agent: credential-helper: usage: sandbox-agent credential-helper <get|store|erase>")
	}

	op := args[0]
	if op != "get" && op != "store" && op != "erase" {
		return fmt.Errorf("sandbox-agent: credential-helper: unknown op %q, want get/store/erase", op)
	}

	cfg, err := boot.Load()
	if err != nil {
		return fmt.Errorf("sandbox-agent: credential-helper: load config: %w", err)
	}

	if op == "store" {
		return credentials.RunStore(os.Stdin)
	}

	cache := &credentials.Cache{Dir: cfg.CredentialCacheDir}

	if op == "erase" {
		return credentials.RunErase(os.Stdin, cache)
	}

	// op == "get" from here on -- the only op that actually needs a live
	// SessionConfig (ControlPlaneWsUrl/SessionId/SandboxToken to mint a
	// fresh credential from CP). store/erase need neither, so gating them
	// on it too would (in some future scenario where erase is invoked
	// without one) block purging a bad cache entry -- working against the
	// very goal RunErase exists for.
	if cfg.SessionConfig == nil {
		return errors.New(
			"sandbox-agent: credential-helper: get: NARVI_SESSION_CONFIG is unset -- nothing to fetch credentials for",
		)
	}

	timeouts := platform.DefaultTimeouts()
	client, err := credentials.NewCPClient(cfg.SessionConfig.ControlPlaneWsUrl, timeouts.CredentialFetchTimeout)
	if err != nil {
		return fmt.Errorf("sandbox-agent: credential-helper: build CP client: %w", err)
	}

	return credentials.RunGet(
		context.Background(), os.Stdin, os.Stdout, cache, client,
		cfg.SessionConfig.SessionId, cfg.SessionConfig.SandboxToken, timeouts.CredentialExpiryBuffer,
	)
}

// run mirrors cmd/control-plane/main.go's serve() shape: a thin main()
// dispatches to this testable, error-returning function.
func run() error {
	// boot.Load() is the earliest possible failure -- before any logging
	// setup even -- exactly like control-plane's own platform.Load()
	// failure path.
	cfg, err := boot.Load()
	if err != nil {
		return fmt.Errorf("sandbox-agent: load config: %w", err)
	}

	logger := platform.NewLogger(os.Stdout, cfg.LogLevel)
	slog.SetDefault(logger)

	// sandbox-agent has no env-driven timeout-override mechanism (neither
	// does control-plane's own platform.Load(), which also always uses
	// DefaultTimeouts() verbatim), so reuse the shared defaults directly.
	timeouts := platform.DefaultTimeouts()

	// §5.3: "sandbox-agent logs a boot fingerprint first" -- this MUST be
	// the very first line this binary emits; nothing above this point
	// logs anything.
	fingerprint := boot.CollectFingerprint(cfg, timeouts.RepoSHADiscoveryTimeout)
	slog.Info("sandbox-agent: boot fingerprint",
		"agent_version", fingerprint.AgentVersion,
		"image_digest", fingerprint.ImageDigest,
		"boot_mode", string(fingerprint.BootMode),
		"repo_shas", fingerprint.RepoSHAs,
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sup := supervisor.New()

	// reportBootProgress is a slog-only ProgressReporter (§6.1's
	// boot_progress event, logged rather than transported anywhere yet).
	// HONEST GAP, same shape as Step 13's own NARVI_IMAGE_DIGEST gap:
	// Step 16 (control-plane WS bridge) is expected to replace/wrap this
	// with a reporter that also forwards each event over the real WS
	// connection once that bridge exists.
	reportBootProgress := func(event services.BootProgressEvent) {
		if event.Phase == services.PhaseFailed {
			slog.Info("sandbox-agent: boot_progress",
				"service", event.ServiceName, "phase", string(event.Phase), "error", event.Err)
			return
		}
		slog.Info("sandbox-agent: boot_progress", "service", event.ServiceName, "phase", string(event.Phase))
	}

	// repos stays nil when no SESSION_CONFIG was delivered (the common
	// dev/test case) -- exactly today's behavior, and RunBoot's
	// documented, correct no-op. When SessionConfig IS present, clone
	// every repo it names (in order) BEFORE running the rest of the boot
	// sequence, write the generated AGENTS.md manifest (§6.4), then build
	// []boot.RepoInfo from only the SUCCESSFULLY cloned results,
	// preserving order/Primary.
	var repos []boot.RepoInfo
	if cfg.SessionConfig != nil {
		results, cloneErr := gitclone.CloneAll(ctx, sup, cfg.WorkspaceDir, cfg.SessionConfig.Repos,
			timeouts.RepoCloneTimeout, timeouts.ProcessStopGracePeriod)
		if cloneErr != nil {
			return fmt.Errorf("sandbox-agent: clone repos: %w", cloneErr)
		}

		if err := gitclone.WriteAgentsManifest(cfg.WorkspaceDir, results); err != nil {
			return fmt.Errorf("sandbox-agent: write AGENTS.md: %w", err)
		}

		// The FIRST boot-fingerprint line above necessarily reported
		// repo_shas as empty -- §5.3 requires it logged first, and nothing
		// was cloned yet at that point. Now that cloning has happened,
		// re-collect and log an updated fingerprint so repo_shas actually
		// carries the information §5.3 asks for on this exact path;
		// nothing about the original "logs first" line is changed or
		// replaced, this is a second, supplementary log line.
		postCloneFingerprint := boot.CollectFingerprint(cfg, timeouts.RepoSHADiscoveryTimeout)
		slog.Info("sandbox-agent: boot fingerprint (post-clone)",
			"repo_shas", postCloneFingerprint.RepoSHAs,
		)

		for _, result := range results {
			if result.Err != nil {
				continue
			}
			repos = append(repos, boot.RepoInfo{Name: result.Repo.Name, Primary: result.Primary})
		}
	}

	if err := boot.RunBoot(ctx, sup, cfg.WorkspaceDir, repos, cfg.BootMode, reportBootProgress,
		timeouts.HookTimeout, timeouts.ProcessStopGracePeriod,
		timeouts.ServiceReadinessTimeout, timeouts.ServiceReadinessPollInterval); err != nil {
		return fmt.Errorf("sandbox-agent: boot: %w", err)
	}

	slog.Info("sandbox-agent: boot sequence complete (partial -- control-plane WS bridge: Step 16; " +
		"OpenCode adapter: Step 17)")

	// Block until told to shut down: a real sandbox-agent process must
	// stay alive/supervising until then -- there is simply nothing else
	// for it to do yet at this Step.
	<-ctx.Done()

	slog.Info("sandbox-agent: shutting down", "grace_period", timeouts.SupervisorShutdownTimeout.String())

	// Deliberately a fresh background context, not ctx: by this point ctx
	// is already canceled (that's exactly what triggers this shutdown),
	// and a canceled context would make StopAll fail immediately instead
	// of actually bounding the drain -- same reasoning as control-plane's
	// own shutdownOTel deferred call.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeouts.SupervisorShutdownTimeout)
	defer cancel()

	return sup.StopAll(shutdownCtx, timeouts.ProcessStopGracePeriod)
}
