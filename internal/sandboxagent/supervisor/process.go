package supervisor

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// ExitResult is the outcome of a supervised process's natural exit.
// ExitCode is meaningful only when Err is nil; Err is non-nil only when
// the process could not even be waited on (a rare, genuine failure of the
// wait syscall itself, not a non-zero exit).
type ExitResult struct {
	ExitCode int
	Err      error
}

// Process is one supervised OS process, running as the leader of its own
// process group (see doc.go's "Mechanics"). Every Process is reaped
// automatically by a background goroutine launched at Spawn time --
// callers never need to call Wait for reaping to happen; Wait/Stop merely
// observe or hasten the outcome.
type Process struct {
	pgid   int
	doneCh chan struct{} // closed exactly once, after result is written
	result ExitResult
}

// Wait blocks until the process has exited (reaped by the background
// goroutine) or ctx is done, whichever comes first.
func (p *Process) Wait(ctx context.Context) (ExitResult, error) {
	select {
	case <-p.doneCh:
		return p.result, nil
	case <-ctx.Done():
		return ExitResult{}, ctx.Err()
	}
}

// Exited reports whether the process has already exited (reaped by the
// background goroutine), returning its ExitResult if so. Unlike Wait, it
// NEVER blocks -- a single non-blocking channel check.
func (p *Process) Exited() (ExitResult, bool) {
	select {
	case <-p.doneCh:
		return p.result, true
	default:
		return ExitResult{}, false
	}
}

// Stop is graceful-then-forceful and bounded. It ALWAYS signals the whole
// process group -- even when the tracked leader has already exited -- a
// leader that backgrounds a descendant before exiting on its own leaves
// that descendant as a live member of the SAME process group, and POSIX
// guarantees a process group's ID is never reused for an unrelated
// process while any member of it remains alive (SUSv4's kill()/setpgid()
// rationale). So -pgid always still safely targets only this group's own
// members, whether or not the original leader is one of them anymore --
// there is never a risk of hitting a coincidentally-recycled, unrelated
// process. An earlier version of this method returned immediately without
// signaling at all once the leader had exited, which silently orphaned
// any descendant the leader had backgrounded -- exactly the bug
// process-group signaling exists to prevent.
//
// If the leader is still running: signals SIGTERM to the group, waits up
// to grace (or until ctx is done, whichever comes first) for it to exit;
// if neither happens in time, escalates to SIGKILL on the whole group and
// then blocks unconditionally until the background reap goroutine
// observes the exit -- SIGKILL cannot be caught or ignored, so this final
// wait is provably bounded by the OS, not by this code.
//
// If the leader has ALREADY exited (doneCh already closed): there is no
// further doneCh-style event to wait on for a surviving descendant --
// Process only tracks the leader itself, and has no visibility into pids
// it never spawned directly. Stop still sends SIGTERM, waits up to grace
// for a well-behaved descendant to clean up on its own, then sends
// SIGKILL -- a best-effort cleanup with no way to confirm a descendant is
// actually gone afterward.
func (p *Process) Stop(ctx context.Context, grace time.Duration) error {
	leaderAlreadyExited := false
	select {
	case <-p.doneCh:
		leaderAlreadyExited = true
	default:
	}

	if err := signalGroup(p.pgid, syscall.SIGTERM); err != nil {
		return err
	}

	timer := time.NewTimer(grace)
	defer timer.Stop()

	if leaderAlreadyExited {
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
		return signalGroup(p.pgid, syscall.SIGKILL)
	}

	select {
	case <-p.doneCh:
		return nil
	case <-ctx.Done():
		// Fall through to the forceful escalation below: a bounded
		// shutdown must not wait out the rest of grace once its own
		// outer deadline has already passed.
	case <-timer.C:
		// grace elapsed with no sign the process exited -- escalate.
	}

	// Any error from the SIGKILL syscall itself (besides ESRCH, already
	// treated as benign inside signalGroup) is intentionally not
	// propagated: whether or not the signal actually delivered, the
	// background reap goroutine launched at Spawn time will still observe
	// the process's eventual exit and close doneCh, so blocking
	// unconditionally below remains correct either way.
	_ = signalGroup(p.pgid, syscall.SIGKILL)

	<-p.doneCh
	return nil
}

// signalGroup sends sig to the entire process group led by pgid (POSIX
// killpg semantics: the negative of the leader's pid). syscall.ESRCH (no
// such process -- it already exited) is a benign no-op, never an error to
// propagate; any other failure is returned as-is.
func signalGroup(pgid int, sig syscall.Signal) error {
	if err := syscall.Kill(-pgid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// exitResultFromWaitErr converts the error cmd.Wait() returned (nil on a
// clean exit(0), *exec.ExitError on any other exit code, something else
// entirely if the process could not even be waited on) into an ExitResult.
func exitResultFromWaitErr(err error) ExitResult {
	if err == nil {
		return ExitResult{ExitCode: 0}
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return ExitResult{ExitCode: exitErr.ExitCode()}
	}

	return ExitResult{Err: err}
}
