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
// (§5.3), runs the boot sequence for whatever (currently empty, until
// Step 15) repo list names, then blocks until told to shut down.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/internal/sandboxagent/boot"
	"github.com/khazaddev/narvi/internal/sandboxagent/services"
	"github.com/khazaddev/narvi/internal/sandboxagent/supervisor"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
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

	// repos is nil/empty until Step 15 populates it from SESSION_CONFIG --
	// multi-repo cloning does not exist yet (this Step's own scope
	// boundary). An empty slice is RunBoot's documented, correct no-op.
	if err := boot.RunBoot(ctx, sup, cfg.WorkspaceDir, nil, cfg.BootMode, reportBootProgress,
		timeouts.HookTimeout, timeouts.ProcessStopGracePeriod,
		timeouts.ServiceReadinessTimeout, timeouts.ServiceReadinessPollInterval); err != nil {
		return fmt.Errorf("sandbox-agent: boot: %w", err)
	}

	slog.Info("sandbox-agent: boot sequence complete (partial -- multi-repo clone: Step 15; " +
		"control-plane WS bridge: Step 16; OpenCode adapter: Step 17)")

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
