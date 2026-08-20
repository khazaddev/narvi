package supervisor

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
)

// Spec describes one command to spawn under supervision.
type Spec struct {
	Path string
	Args []string
	Dir  string
	// Env is the spawned process's environment. A nil Env means "inherit
	// this process's own environment" (exec.Cmd's own documented
	// behavior when Env is nil) -- callers that want a clean or
	// augmented environment must build the full slice themselves.
	Env []string

	// Stdout and Stderr, when non-nil, receive the spawned process's own
	// standard output/error streams (exec.Cmd's own Stdout/Stderr fields,
	// passed through verbatim). Both default to nil -- exactly every
	// EXISTING call site's own zero value -- which discards that stream
	// entirely (exec.Cmd's own documented behavior for a nil Writer),
	// identical to this package's behavior before these two fields
	// existed. Added (§9.3, "e2e happy path") for a caller that needs
	// a short-lived command's own OUTPUT, not just its exit code (e.g.
	// `git rev-parse HEAD` for the resulting push SHA) -- Process itself
	// still only ever tracks ExitResult; capturing output is entirely the
	// caller's own choice of io.Writer (typically a bytes.Buffer), never
	// buffered or exposed by Supervisor itself.
	Stdout io.Writer
	Stderr io.Writer
}

// Supervisor tracks a set of concurrently-running Processes, each spawned
// into its own process group (see doc.go's "Mechanics").
type Supervisor struct {
	mu        sync.Mutex
	processes []*Process

	// group exists solely so the per-process background reap goroutine
	// (launched in Spawn) has an errgroup.Group.Go call site to go
	// through instead of a bare `go` statement (§11: no naked
	// goroutines) -- it does NOT provide an "every reap goroutine has
	// finished" guarantee: nothing ever calls group.Wait(). That
	// guarantee is unnecessary here because each Process's own doneCh
	// (closed by its reap goroutine immediately before the goroutine
	// returns) is what Wait/Stop/StopAll actually synchronize on.
	// Deliberately NOT wired up to a Wait() call: doing so safely would
	// require guaranteeing no concurrent Spawn (i.e. no concurrent
	// group.Go) is in flight, which this Supervisor does not assume --
	// see sync.WaitGroup's own "Add with a positive delta that starts
	// when the counter could be zero must happen before a Wait" rule. A
	// future maintainer should not "fix" this by adding a naive
	// group.Wait() call without first addressing that race.
	// Deliberately still the zero value, NOT errgroup.WithContext(...),
	// for the same reason as internal/app/sessionactor/registry.go's own
	// `group` field and this package's own StopAll fan-out below:
	// nothing here should ever share a cancel-on-first-error context
	// with a resource whose failures must stay independent.
	group errgroup.Group
}

// New returns an empty Supervisor ready to Spawn processes.
func New() *Supervisor {
	return &Supervisor{}
}

// Spawn starts spec as the leader of its own new process group (so a
// signal to the group reaches any subprocess it forks too), registers it
// with this Supervisor, and launches its background reap goroutine (via
// this Supervisor's own errgroup.Group.Go -- never a bare `go` statement)
// that unconditionally waits on it exactly once, so it is reaped promptly
// even if no caller ever calls Wait or Stop on the returned Process.
func (s *Supervisor) Spawn(spec Spec) (*Process, error) {
	cmd := exec.Command(spec.Path, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	cmd.Stdout = spec.Stdout
	cmd.Stderr = spec.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("supervisor: start %s: %w", spec.Path, err)
	}

	// Setpgid with no Pgid set makes the new process its own group leader
	// -- its pgid equals its own pid.
	proc := &Process{
		pgid:   cmd.Process.Pid,
		doneCh: make(chan struct{}),
	}

	s.mu.Lock()
	s.processes = append(s.processes, proc)
	s.mu.Unlock()

	s.group.Go(func() error {
		waitErr := cmd.Wait()
		proc.result = exitResultFromWaitErr(waitErr)
		close(proc.doneCh)
		// The reap goroutine itself never fails: a child's own non-zero
		// exit or wait failure is reported via ExitResult on the
		// Process, not by failing this shared errgroup (which no caller
		// ever inspects for errors -- see the group field's own comment
		// above).
		return nil
	})

	return proc, nil
}

// StopAll bounds a graceful-then-forceful shutdown of every process this
// Supervisor currently tracks, run CONCURRENTLY -- one Stop call per
// process, fanned out via a SEPARATE, zero-value errgroup.Group. This
// mirrors internal/app/sessionactor/registry.go's own `group` field for
// the same reason: if process A's Stop call returns a genuine error (a
// real syscall failure), that must never cancel a shared context and
// truncate process B's own independent grace period -- each process's
// grace period is its own bounded thing, unrelated to whether a SIBLING
// process's stop attempt succeeded.
func (s *Supervisor) StopAll(ctx context.Context, grace time.Duration) error {
	s.mu.Lock()
	procs := make([]*Process, len(s.processes))
	copy(procs, s.processes)
	s.mu.Unlock()

	var fanout errgroup.Group

	for _, proc := range procs {
		fanout.Go(func() error {
			return proc.Stop(ctx, grace)
		})
	}

	return fanout.Wait()
}
