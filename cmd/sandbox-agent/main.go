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
//
// Step 16 wires the real sandbox WS bridge (internal/sandboxagent/wsbridge)
// in place of the slog-only boot_progress reporter Step 14 left as an
// explicit placeholder: when Config.SessionConfig is present, run() builds
// a *wsbridge.Bridge and drives it via bridge.Run(ctx) alongside the
// existing OS-signal-driven shutdown -- whichever finishes first (an OS
// signal cancels ctx, or the control plane sends a "shutdown" command, or
// the handshake returns a fatal 401/403/404/410 status) converges on the
// SAME StopAll-based graceful shutdown Step 13 built, except a fatal
// connect status propagates as run()'s own error instead. prompt/stop/push/
// snapshot/git_sync_complete are wired to a log-only stub handler --
// implementing what any of them actually DO is Step 17 (OpenCode adapter,
// prompt/stop), Step 21 (e2e happy path, push), Step 22 (snapshots &
// restore, snapshot), and Step 29 (gitstate in-sandbox, git_sync_complete).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sync/errgroup"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/internal/sandboxagent/boot"
	"github.com/khazaddev/narvi/internal/sandboxagent/credentials"
	"github.com/khazaddev/narvi/internal/sandboxagent/gitclone"
	"github.com/khazaddev/narvi/internal/sandboxagent/services"
	"github.com/khazaddev/narvi/internal/sandboxagent/supervisor"
	"github.com/khazaddev/narvi/internal/sandboxagent/wsbridge"
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

// commandHandler is Step 16's own log-only wsbridge.CommandHandler
// implementation: the WS transport/ack/reconnect/dispatch machinery is
// this Step's real, tested deliverable (internal/sandboxagent/wsbridge) --
// what any of these 5 commands actually DOES is each cited Step's own
// job, confirmed against docs/IMPLEMENTATION_PLAN.md rather than guessed.
type commandHandler struct{}

func (commandHandler) HandlePrompt(_ context.Context, cmd sandboxws.Prompt) {
	slog.Info("sandbox-agent: received prompt, not yet implemented (Step 17: OpenCode adapter)",
		"messageId", cmd.MessageId)
}

func (commandHandler) HandleStop(_ context.Context, cmd sandboxws.Stop) {
	slog.Info("sandbox-agent: received stop, not yet implemented (Step 17: OpenCode adapter)",
		"messageId", cmd.MessageId)
}

func (commandHandler) HandlePush(_ context.Context, cmd sandboxws.Push) {
	slog.Info("sandbox-agent: received push, not yet implemented (Step 21: e2e happy path)",
		"messageId", cmd.MessageId)
}

func (commandHandler) HandleSnapshot(_ context.Context, cmd sandboxws.Snapshot) {
	slog.Info("sandbox-agent: received snapshot, not yet implemented (Step 22: snapshots & restore)",
		"messageId", cmd.MessageId)
}

func (commandHandler) HandleGitSyncComplete(_ context.Context, cmd sandboxws.GitSyncComplete) {
	slog.Info("sandbox-agent: received git_sync_complete, not yet implemented (Step 29: gitstate in-sandbox)",
		"messageId", cmd.MessageId)
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

	// bridge is nil exactly when cfg.SessionConfig is nil (the common dev/
	// test case with no real session) -- everything below that branches on
	// "bridge != nil" preserves today's original no-bridge behavior
	// unchanged in that case. sandboxID is this Step's own HONEST-GAP value
	// (Config.SandboxID's own doc comment).
	var bridge *wsbridge.Bridge
	if cfg.SessionConfig != nil {
		bridge = wsbridge.New(*cfg.SessionConfig, cfg.SandboxID, commandHandler{},
			timeouts.SandboxWSDialTimeout, timeouts.SandboxWSHeartbeatInterval,
			timeouts.SandboxWSReconnectMinBackoff, timeouts.SandboxWSReconnectMaxBackoff)
	}

	// reportBootProgress forwards each §6.1 boot_progress event over the
	// real WS bridge when one exists (a live session) -- AND always logs a
	// service-level failure locally too, regardless of whether a bridge
	// exists: an earlier version of this Step only logged event.Err in the
	// nil-bridge fallback branch, silently dropping the diagnostic reason
	// for a real service-boot failure whenever a live bridge was present
	// (the wire boot_progress event itself has no error field to carry it
	// either, so the local log is the ONLY place this information survives
	// at all on that path).
	reportBootProgress := func(event services.BootProgressEvent) {
		if bridge != nil {
			if sendErr := bridge.SendBootProgress(ctx, event); sendErr != nil {
				slog.Warn("sandbox-agent: send boot_progress over WS bridge failed",
					"service", event.ServiceName, "phase", string(event.Phase), "error", sendErr)
			}
		}
		if event.Phase == services.PhaseFailed {
			slog.Info("sandbox-agent: boot_progress",
				"service", event.ServiceName, "phase", string(event.Phase), "error", event.Err)
			return
		}
		if bridge == nil {
			slog.Info("sandbox-agent: boot_progress", "service", event.ServiceName, "phase", string(event.Phase))
		}
	}

	// Start the WS bridge (or, when there's no live session, the equivalent
	// ctx-wait goroutine) BEFORE cloning/booting -- not after. An earlier
	// version of this Step only launched bridge.Run once runBootSequence
	// below had already returned, which meant NO WS connection existed for
	// the entire boot window: nothing reportBootProgress sent during
	// cloning/hooks/services would reach the control plane until boot was
	// already fully done, silently defeating §3.2's "boot-progress reports
	// re-arm the connecting deadline" rule and resilience scenario §9.3 #3
	// (a slow boot must not cause a false kill). Bridge.Run's own outbound
	// buffer already handles "SendBootProgress called before the connection
	// has finished dialing" gracefully (buffer now, flush once connected),
	// so starting it concurrently with the boot sequence costs nothing and
	// fixes a real bug. A single errgroup.Group either way (no naked `go`
	// statement, §11): the nil-bridge branch's own goroutine does exactly
	// what a direct `<-ctx.Done()` would, just launched through the group
	// so both cases converge identically below.
	var group errgroup.Group
	if bridge != nil {
		group.Go(func() error {
			return bridge.Run(ctx)
		})
	} else {
		group.Go(func() error {
			<-ctx.Done()
			return nil
		})
	}

	bootErr := runBootSequence(ctx, sup, cfg, timeouts, reportBootProgress)
	if bootErr != nil {
		// A boot failure needs the same graceful convergence a fatal WS
		// status or an OS signal gets -- cancel ctx (stop is a genuine
		// context.CancelFunc, signal.NotifyContext's own doc comment;
		// calling it more than once, e.g. again via the deferred call
		// above, is safe) so the bridge/ctx-wait goroutine above actually
		// unwinds instead of running forever waiting for a reason to stop
		// that a boot failure alone would never give it.
		stop()
	} else {
		if bridge != nil {
			bridge.MarkBootComplete()
		}
		slog.Info("sandbox-agent: boot sequence complete (partial -- OpenCode adapter: Step 17)")
	}

	runErr := group.Wait()

	// Always attempt a bounded graceful shutdown of every supervised
	// process, regardless of why the above finished -- a fatal WS status or
	// a boot failure must not skip cleanup and orphan whatever hooks/
	// services already started (Setpgid'd process groups have no
	// Pdeathsig; nothing else reaps them if sandbox-agent exits without
	// running this).
	slog.Info("sandbox-agent: shutting down", "grace_period", timeouts.SupervisorShutdownTimeout.String())

	// Deliberately a fresh background context, not ctx: by this point ctx
	// is already canceled (that's exactly what triggers this shutdown),
	// and a canceled context would make StopAll fail immediately instead
	// of actually bounding the drain -- same reasoning as control-plane's
	// own shutdownOTel deferred call.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeouts.SupervisorShutdownTimeout)
	defer cancel()
	stopErr := sup.StopAll(shutdownCtx, timeouts.ProcessStopGracePeriod)

	// A fatal handshake status (401/403/404/410, §6.1: "no retry") is NOT a
	// normal shutdown trigger -- it must propagate as run()'s own error
	// (main() then os.Exit(1)s) even though StopAll above already ran.
	var fatalErr *wsbridge.FatalConnectError
	if errors.As(runErr, &fatalErr) {
		return fmt.Errorf("sandbox-agent: WS bridge: %w", runErr)
	}

	// Every other outcome -- wsbridge.ErrShutdownRequested (a CP-issued
	// "shutdown" command), nil, or ctx.Err() (an OS signal via
	// signal.NotifyContext, or the boot-failure-triggered stop() above) --
	// is a normal convergence path, not logged as unexpected.
	if runErr != nil && !errors.Is(runErr, wsbridge.ErrShutdownRequested) && !errors.Is(runErr, context.Canceled) {
		// Not expected per Bridge.Run's own documented contract (only nil,
		// ctx.Err(), ErrShutdownRequested, or *FatalConnectError -- already
		// handled above -- are ever returned), but logged defensively
		// rather than silently ignored if that contract is ever violated.
		slog.Warn("sandbox-agent: WS bridge Run returned an unexpected error", "error", runErr)
	}

	if bootErr != nil {
		return fmt.Errorf("sandbox-agent: boot: %w", bootErr)
	}
	return stopErr
}

// runBootSequence clones every repo cfg.SessionConfig names (in order),
// writes the generated AGENTS.md manifest (§6.4), then runs boot.RunBoot
// against the successfully-cloned subset. repos/cloning is skipped
// entirely when cfg.SessionConfig is nil (the common dev/test case) --
// boot.RunBoot's own documented, correct no-op on an empty repo list
// handles that unchanged from Step 14.
func runBootSequence(
	ctx context.Context,
	sup *supervisor.Supervisor,
	cfg boot.Config,
	timeouts platform.Timeouts,
	reportBootProgress services.ProgressReporter,
) error {
	var repos []boot.RepoInfo
	if cfg.SessionConfig != nil {
		results, cloneErr := gitclone.CloneAll(ctx, sup, cfg.WorkspaceDir, cfg.SessionConfig.Repos,
			timeouts.RepoCloneTimeout, timeouts.ProcessStopGracePeriod)
		if cloneErr != nil {
			return fmt.Errorf("clone repos: %w", cloneErr)
		}

		if err := gitclone.WriteAgentsManifest(cfg.WorkspaceDir, results); err != nil {
			return fmt.Errorf("write AGENTS.md: %w", err)
		}

		// The FIRST boot-fingerprint line (in run(), above this function)
		// necessarily reported repo_shas as empty -- §5.3 requires it
		// logged first, and nothing was cloned yet at that point. Now that
		// cloning has happened, re-collect and log an updated fingerprint
		// so repo_shas actually carries the information §5.3 asks for on
		// this exact path; nothing about the original "logs first" line is
		// changed or replaced, this is a second, supplementary log line.
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
		return fmt.Errorf("boot: %w", err)
	}
	return nil
}
