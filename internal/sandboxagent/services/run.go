package services

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/khazaddev/narvi/internal/domain/servicemanifest"
	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/internal/sandboxagent/supervisor"
)

// Phase names one point in a service's boot lifecycle, mirroring §6.1's
// "boot_progress" event.
type Phase string

const (
	// PhaseStarting is reported immediately after Spawn succeeds, before
	// readiness resolves either way.
	PhaseStarting Phase = "starting"
	// PhaseReady is reported once the service's readiness check (port
	// dial or HTTP health GET) succeeds.
	PhaseReady Phase = "ready"
	// PhaseFailed is reported when the process exited (crashed) before
	// ever becoming ready -- see doc.go's foreground-only assumption for
	// why ANY exit, even a clean one, counts as a failure here.
	PhaseFailed Phase = "failed"
	// PhaseTimeout is reported when readiness never succeeded within the
	// bound; the process is presumably still running (this package never
	// stops it proactively).
	PhaseTimeout Phase = "timeout"
)

// BootProgressEvent is one point-in-time report for one service, handed to
// a ProgressReporter.
type BootProgressEvent struct {
	ServiceName string
	Phase       Phase
	// Err is non-nil only when Phase is PhaseFailed.
	Err error
}

// ProgressReporter is a plain callback, not an interface -- §6.1 is
// expected to supply one that forwards over the WS bridge once it exists;
// for now main.go supplies a slog-only one. A func type (like
// http.HandlerFunc) is the right amount of ceremony for a single-method
// callback with exactly one real implementation today.
type ProgressReporter func(BootProgressEvent)

// spawnedService pairs one manifest entry with its already-spawned
// process, so the readiness fan-out below can report by name and the
// post-group.Wait() criticality pass can inspect each outcome together
// with the service's own declared Criticality.
type spawnedService struct {
	service servicemanifest.Service
	proc    *supervisor.Process
}

// serviceOutcome is one service's fully resolved boot result, filled in by
// the per-service readiness goroutine and read back only after
// group.Wait() returns -- never concurrently written and read.
type serviceOutcome struct {
	service servicemanifest.Service
	phase   Phase
	err     error // non-nil only when phase == PhaseFailed
}

// Run spawns every declared service CONCURRENTLY (they are meant to run
// alongside each other -- e.g. a frontend dev server and a mock API -- NOT
// sequentially like RunHooks' one-at-a-time model), reports PhaseStarting
// for each immediately after its own Spawn succeeds, waits for each one's
// own readiness bounded by readinessTimeout (polled every
// readinessPollInterval), reports exactly one of
// PhaseReady/PhaseFailed/PhaseTimeout per service once resolved, then
// applies §14.2's criticality semantics:
//
//   - If ANY primary service did not reach PhaseReady, Run returns a
//     combined error (errors.Join) naming every failed primary -- WITHOUT
//     calling StopAll here, exactly matching
//     internal/sandboxagent/boot.RunHooks' own fatal path: main.go's
//     existing shutdown path is what eventually calls StopAll.
//   - If a SECONDARY service did not reach PhaseReady, Run logs a warning
//     (platform.Logger(ctx)) and does not return an error for it -- the
//     service is left running; it may still become ready later, and this
//     package never proactively Stops it.
//
// A Spawn failure itself (e.g. the shell is unavailable) is always fatal
// and returned immediately, regardless of the failing service's own
// criticality -- Run cannot even begin tracking that service's readiness
// without a live process to poll.
//
// env is assigned verbatim to every spawned service's own
// supervisor.Spec.Env (nil means inherit -- exec.Cmd's own documented
// default -- matching every call site's behavior before this parameter
// existed). This package deliberately stays OBLIVIOUS to which specific
// env vars a caller chose to exclude: internal/sandboxagent/boot (this
// Step's real caller, via RunBoot) imports this package already (see
// doc.go), so THIS package importing boot back -- to reference
// boot.SessionConfigEnvVar directly, the way opencodeproc.Spawn's own
// analogous call does -- would create an import cycle. Accepting a plain,
// already-built env slice sidesteps that entirely: RunBoot computes
// supervisor.EnvWithout(SessionConfigEnvVar) itself (same package as the
// constant) and hands the result down here, so a repo's own
// services.yml command still never inherits the sandbox's own plaintext
// bearer token, without this package ever needing to know the env var's
// name.
func Run(
	ctx context.Context,
	sup *supervisor.Supervisor,
	repoDir string,
	manifest servicemanifest.Manifest,
	env []string,
	reporter ProgressReporter,
	readinessTimeout, readinessPollInterval time.Duration,
) error {
	spawned := make([]spawnedService, 0, len(manifest.Services))
	for _, svc := range manifest.Services {
		proc, err := sup.Spawn(supervisor.Spec{
			Path: "/bin/sh",
			Args: []string{"-c", svc.Cmd},
			Dir:  filepath.Join(repoDir, svc.Cwd),
			Env:  env,
		})
		if err != nil {
			return fmt.Errorf("services: spawn %q: %w", svc.Name, err)
		}

		reporter(BootProgressEvent{ServiceName: svc.Name, Phase: PhaseStarting})
		spawned = append(spawned, spawnedService{service: svc, proc: proc})
	}

	outcomes := make([]serviceOutcome, len(spawned))

	// Deliberately a zero-value errgroup.Group, NOT errgroup.WithContext:
	// one service's readiness outcome must never cancel a sibling's own
	// independent readiness wait -- the same reasoning as
	// internal/sandboxagent/supervisor.Supervisor.StopAll's own fan-out
	// and internal/app/sessionactor/registry.go's `group` field. Every
	// service resolves fully (Ready/Failed/Timeout) before this function
	// decides anything about criticality/fatal handling below.
	var group errgroup.Group
	for i, sp := range spawned {
		i, sp := i, sp
		group.Go(func() error {
			check := readinessCheck(sp.service.Readiness)
			phase, err := waitReady(ctx, sp.proc, check, readinessTimeout, readinessPollInterval)

			outcomes[i] = serviceOutcome{service: sp.service, phase: phase, err: err}
			reporter(BootProgressEvent{ServiceName: sp.service.Name, Phase: phase, Err: err})
			return nil
		})
	}
	_ = group.Wait() // never returns an error: the goroutine above always returns nil.

	var fatalErrs []error
	for _, outcome := range outcomes {
		if outcome.phase == PhaseReady {
			continue
		}

		if outcome.service.Criticality == servicemanifest.CriticalityPrimary {
			fatalErrs = append(fatalErrs, fmt.Errorf(
				"services: primary service %q did not become ready (phase=%s): %w",
				outcome.service.Name, outcome.phase, causeOrPhase(outcome),
			))
			continue
		}

		platform.Logger(ctx).Warn("services: secondary service did not become ready, continuing",
			"service", outcome.service.Name, "phase", string(outcome.phase), "error", outcome.err)
	}

	if len(fatalErrs) > 0 {
		return errors.Join(fatalErrs...)
	}
	return nil
}

// causeOrPhase returns outcome.err when set (PhaseFailed always sets it),
// or a synthetic, still-descriptive error naming the phase itself
// (PhaseTimeout never sets an err, by BootProgressEvent's own contract) --
// so the fatal error Run returns is never missing a cause.
func causeOrPhase(outcome serviceOutcome) error {
	if outcome.err != nil {
		return outcome.err
	}
	return fmt.Errorf("readiness check never succeeded within the timeout (phase=%s)", outcome.phase)
}

// waitReady polls check (bounded by timeout, retried every pollInterval)
// until it reports ready, the process exits first (a crash, per this
// package's foreground-only assumption -- see doc.go), or the timeout
// expires, whichever comes first. It uses Process.Exited() -- a
// non-blocking check -- in a simple poll loop rather than racing
// Process.Wait against a ticker in a second nested goroutine, which would
// need its own errgroup for no benefit now that Exited() exists.
func waitReady(
	ctx context.Context,
	proc *supervisor.Process,
	check func(context.Context) bool,
	timeout, pollInterval time.Duration,
) (Phase, error) {
	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		if result, exited := proc.Exited(); exited {
			return PhaseFailed, exitErr(result)
		}
		if check(readyCtx) {
			return PhaseReady, nil
		}
		select {
		case <-readyCtx.Done():
			return PhaseTimeout, nil
		case <-ticker.C:
		}
	}
}

// exitErr turns a supervisor.ExitResult (necessarily an unexpected exit,
// since waitReady only calls this when the process ended before ever
// becoming ready) into a real, descriptive error: result.Err when the wait
// syscall itself failed, "exited with code N" for a non-zero exit, and a
// generic message even for a clean exit(0) -- per this package's
// foreground-only assumption (doc.go), ANY exit before readiness is a
// failure here, code 0 included.
func exitErr(result supervisor.ExitResult) error {
	if result.Err != nil {
		return result.Err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("exited with code %d", result.ExitCode)
	}
	return errors.New("exited unexpectedly before becoming ready")
}

// readinessCheck selects the readiness-check function for one service's
// declared Readiness -- exactly one of Port/Health is ever set, per
// servicemanifest.Validate's own validation.
func readinessCheck(r servicemanifest.Readiness) func(context.Context) bool {
	switch {
	case r.Port != nil:
		return portReady(*r.Port)
	case r.Health != nil:
		return healthReady(*r.Health)
	default:
		// Unreachable: servicemanifest.Validate guarantees exactly one of
		// Port/Health is set on every Readiness it returns.
		return func(context.Context) bool { return false }
	}
}

// portReady reports ready once a TCP dial to 127.0.0.1:port succeeds
// (closing the connection immediately after).
func portReady(port int) func(context.Context) bool {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	return func(ctx context.Context) bool {
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}
}

// healthReady reports ready once an HTTP GET against url returns a 2xx
// status. It deliberately does NOT set an http.Client-level timeout: the
// request's own context (readyCtx, already bounded by waitReady's overall
// readinessTimeout) is sufficient bounding for each individual attempt, and
// adding a THIRD timeout knob (beyond the existing overall timeout and poll
// interval) for a per-request bound that the context already provides
// would be redundant ceremony, not a missing safeguard.
func healthReady(url string) func(context.Context) bool {
	return func(ctx context.Context) bool {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return false
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()

		return resp.StatusCode >= 200 && resp.StatusCode < 300
	}
}
