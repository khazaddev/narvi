// Command sandbox-agent is the static binary shipped into sandbox images
// (§1). Step 13 gives it its first real behavior: boot-mode/hook-policy
// decisions (internal/domain/sandboxboot) plus native process supervision
// (internal/sandboxagent/supervisor) -- process groups, killpg-style
// signaling, reaping, and bounded graceful-then-forceful shutdown,
// orchestrated by internal/sandboxagent/boot. It logs a boot fingerprint
// first (§5.3), runs whatever boot hooks the (currently empty, until
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

	// repos is nil/empty until Step 15 populates it from SESSION_CONFIG --
	// multi-repo cloning does not exist yet (this Step's own scope
	// boundary). An empty slice is RunHooks' documented, correct no-op.
	if err := boot.RunHooks(ctx, sup, cfg.WorkspaceDir, nil, cfg.BootMode,
		timeouts.HookTimeout, timeouts.ProcessStopGracePeriod); err != nil {
		return fmt.Errorf("sandbox-agent: boot hooks: %w", err)
	}

	slog.Info("sandbox-agent: boot sequence complete (partial -- multi-repo clone: Step 15; " +
		"services.yml supervision: Step 14; control-plane WS bridge: Step 16; OpenCode adapter: Step 17)")

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
