// This file (docker.go) implements §27.5's own in-sandbox half of
// Docker-in-sandbox (§27.5): "sandbox-agent supervises dockerd as one
// more entry in the same process-supervision table as everything else
// (§14.2's own 'no new supervision code path' rule), with a named
// boot_progress phase; the CLI/engine binaries come from the toolchain
// image (§27.7) and the daemon simply never starts when the flag is
// off."
//
// "The same process-supervision table" means the SAME *supervisor.
// Supervisor every other sandbox-agent-managed process (OpenCode,
// code-server, ttyd, every services.yml entry) is registered on — Spawn
// is called on that SAME instance below, so dockerd is reaped/stopped by
// StopAll exactly like everything else, with no separate lifecycle of
// its own. "A named boot_progress phase" means this reuses
// internal/sandboxagent/services' own BootProgressEvent/Phase/
// ProgressReporter vocabulary verbatim (the same §6.1 wire event every
// services.yml entry already reports through) rather than inventing a
// second event shape — no contract change was needed for this Step.
//
// RunDocker does NOT reuse services.Run itself: that function's own
// Readiness shape (servicemanifest.Readiness) is Port/HTTP-only, which
// does not fit dockerd's own default behavior (a Unix domain socket, no
// HTTP API without deliberately opening one). Rather than stretch
// services.yml's own repo-authored schema to accommodate a socket-based
// readiness check no repo-authored service plausibly needs, this file
// implements the identical poll-until-ready-or-timeout SHAPE directly
// against dockerd's own real readiness signal (its socket file
// appearing) — see runDockerReady below, which intentionally mirrors
// services.waitReady's own structure closely enough to read as "the same
// idea, applied to a different readiness primitive," not a fork of
// unrelated logic.

package boot

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/narvidev/narvi/internal/sandboxagent/services"
	"github.com/narvidev/narvi/internal/sandboxagent/supervisor"
)

// dockerdServiceName is the boot_progress "service" name RunDocker
// reports under — see this file's own top doc comment.
const dockerdServiceName = "dockerd"

// DefaultDockerdBinary is the dockerd binary RunDocker spawns by default
// — the toolchain image (§27.7) bakes the Docker CLI/engine binaries
// onto PATH; nothing about their presence there starts the daemon on its
// own — RunDocker is the ONE thing that ever execs dockerd, and it is
// only ever called when SessionConfig.Docker is true (see this file's
// own top doc comment: "the daemon simply never starts when the flag is
// off").
const DefaultDockerdBinary = "dockerd"

// DefaultDockerSocketPath is dockerd's own documented default Unix
// socket path — RunDocker's readiness check polls for this path to
// exist. See this file's own top doc comment for why a socket-file poll,
// not an HTTP/TCP check, is this function's own readiness primitive:
// dockerd exposes no HTTP API by default, and opening one on a local TCP
// port purely for a readiness probe would add an unauthenticated Docker
// control surface (root-equivalent container control) this design has
// no reason to ever need.
const DefaultDockerSocketPath = "/var/run/docker.sock"

// RunDocker supervises dockerd for this sandbox (§27.5). Called ONCE per
// boot (a session-level daemon, not scoped to any one repo — unlike
// RunBoot's own per-repo loop), and ONLY when the session's own
// SessionConfig.Docker is true.
//
// sup is the SAME *supervisor.Supervisor every other sandbox-agent-
// managed process already shares (this file's own top doc comment: "one
// more entry in the same process-supervision table"). dockerdBinary/
// socketPath are parameterized (rather than hardcoded to
// DefaultDockerdBinary/DefaultDockerSocketPath) purely for testability —
// every real caller passes the two Default* constants; a test can point
// both at a small fake script and a temp file, standing in for a real
// dockerd with no actual Docker runtime required (mirroring this
// codebase's own established "no real X reachable in this environment"
// precedent, e.g. internal/adapters/outbound/modal's own fake httptest.
// Server).
//
// env is threaded straight through to the spawned process's own
// environment (never os.Setenv onto sandbox-agent's own process, per
// this codebase's own "env injection is threaded, never os.Setenv"
// convention) — the caller (cmd/sandbox-agent/main.go) passes the SAME
// supervisor.EnvWithout(SessionConfigEnvVar)-filtered, secretEnv-
// appended slice RunBoot's own per-repo services.yml/hook spawns
// already use, so a customer's own configured HTTP_PROXY/HTTPS_PROXY/
// NO_PROXY secrets (§27.6's own cooperative-routing mechanism, §27.1)
// route dockerd's own image pulls through a configured proxy exactly
// like every other spawned process already does — cooperative, not
// enforced, the same honest limit §27.6 states for every other consumer
// of those env vars.
//
// A dockerd that never becomes ready (crashes, or never creates its
// socket within readinessTimeout) is FATAL, returned as a real error —
// unlike a `secondary` services.yml entry, there is no lesser
// criticality available for the ONE daemon a `docker: required`
// Environment exists to guarantee: a session that asked for Docker and
// silently got none is worse than a session that fails to boot loudly
// (mirroring §10-P2's "never silently unenforced" reasoning, applied
// here to the in-sandbox half of the same guarantee the dispatch-time
// provider-capability re-check already applies control-plane-side).
func RunDocker(
	ctx context.Context,
	sup *supervisor.Supervisor,
	dockerdBinary, socketPath string,
	env []string,
	reporter services.ProgressReporter,
	readinessTimeout, readinessPollInterval time.Duration,
) error {
	reporter(services.BootProgressEvent{ServiceName: dockerdServiceName, Phase: services.PhaseStarting})

	proc, err := sup.Spawn(supervisor.Spec{
		Path: dockerdBinary,
		// No Args, deliberately: dockerd's own documented default already
		// listens on DefaultDockerSocketPath when unconfigured. A future
		// caller needing a non-default socketPath here would ALSO need to
		// pass a matching "--host=unix://<socketPath>" arg for the two to
		// stay consistent — not built today because every real caller
		// uses the one default path.
		Env: env,
	})
	if err != nil {
		wrapped := fmt.Errorf("boot: spawn dockerd: %w", err)
		reporter(services.BootProgressEvent{ServiceName: dockerdServiceName, Phase: services.PhaseFailed, Err: wrapped})
		return wrapped
	}

	phase, err := runDockerReady(ctx, proc, socketPath, readinessTimeout, readinessPollInterval)
	reporter(services.BootProgressEvent{ServiceName: dockerdServiceName, Phase: phase, Err: err})
	if err != nil {
		return fmt.Errorf("boot: dockerd did not become ready: %w", err)
	}
	return nil
}

// runDockerReady polls for socketPath to exist (bounded by timeout,
// retried every pollInterval) until it appears, the process exits first
// (a crash), or the timeout expires, whichever comes first — mirrors
// internal/sandboxagent/services' own waitReady structure (this file's
// own top doc comment), applied to a socket-file-existence check instead
// of a port dial/HTTP GET.
func runDockerReady(
	ctx context.Context,
	proc *supervisor.Process,
	socketPath string,
	timeout, pollInterval time.Duration,
) (services.Phase, error) {
	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		if result, exited := proc.Exited(); exited {
			return services.PhaseFailed, dockerdExitErr(result)
		}
		if _, statErr := os.Stat(socketPath); statErr == nil {
			return services.PhaseReady, nil
		}
		select {
		case <-readyCtx.Done():
			return services.PhaseTimeout, fmt.Errorf("dockerd did not create its socket at %q within %s", socketPath, timeout)
		case <-ticker.C:
		}
	}
}

// dockerdExitErr turns a supervisor.ExitResult (necessarily an
// unexpected exit, since runDockerReady only calls this when the process
// ended before ever becoming ready) into a real, descriptive error —
// mirrors services.exitErr's own identical shape exactly.
func dockerdExitErr(result supervisor.ExitResult) error {
	if result.Err != nil {
		return result.Err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("dockerd exited with code %d", result.ExitCode)
	}
	return fmt.Errorf("dockerd exited unexpectedly before becoming ready")
}
