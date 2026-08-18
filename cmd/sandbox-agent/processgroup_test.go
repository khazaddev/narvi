// This file is deliberately NOT behind the "integration" build tag (unlike
// push_integration_test.go): stopProcessGroup/signalProcessGroup are used
// by push_integration_test.go's own runSandboxAgent (integration-only,
// drives the real compiled sandbox-agent binary), but this file's own
// regression test below exercises them directly against a fast, fake
// /bin/sh stand-in -- mirroring internal/sandboxagent/supervisor/
// supervisor_test.go's own spawnShell precedent -- so it can run under the
// default `go test ./...`/`go test -race` suite, not just `make
// test-integration`. Keeping stopProcessGroup/signalProcessGroup in this
// untagged file (rather than push_integration_test.go itself) is what
// makes that possible: an untagged file is always compiled in, regardless
// of which build tags are active, so both this file's own fast test and
// push_integration_test.go's real-binary test can reference the same two
// functions.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"
)

// stopProcessGroup is the test-side mirror of internal/sandboxagent/
// supervisor.Process.Stop's own graceful-then-forceful shutdown
// (internal/sandboxagent/supervisor/process.go), applied to a real OS
// process this test spawned directly via exec.Command/exec.CommandContext
// rather than through a *supervisor.Process handle -- there is no
// *supervisor.Process here to call Stop on, since the process under test
// (e.g. the real, separately-compiled sandbox-agent binary in
// push_integration_test.go) is not itself supervised by anything in THIS
// test binary.
//
// pgid must be the process group leader's own pid (i.e. the process must
// have been started with SysProcAttr{Setpgid: true} and no Pgid set, so
// its pgid equals its own pid -- exactly runSandboxAgent's own
// convention). Sends SIGTERM to the whole group, waits up to grace (or
// until waitDone fires first, whichever comes first), then UNCONDITIONALLY
// escalates to SIGKILL on the whole group and blocks on waitDone: SIGKILL
// cannot be caught or ignored, so that final wait is bounded by the OS,
// not by this code.
//
// The SIGKILL sweep runs even when waitDone fires first -- i.e. even when
// the tracked LEADER has already exited. waitDone is closed by a single
// cmd.Wait() on the leader alone; it carries no information about
// descendants the leader backgrounded into the same process group before
// exiting; a well-behaved leader has already stopped them itself by the
// time it exits, in which case the group is empty and this sweep is a
// harmless no-op (signalProcessGroup treats ESRCH as benign). But if the
// leader exits -- gracefully, by crash, or by dying to the initial SIGTERM
// itself, before its own descendants have been reaped -- while one of
// those descendants is still ignoring SIGTERM, only this unconditional
// sweep terminates it: POSIX guarantees a process group's pgid is never
// reused for an unrelated process while any member of it remains alive
// (SUSv4's kill()/setpgid() rationale), so -pgid still safely targets only
// this group's own members even after the original leader is gone. An
// earlier version of this function returned immediately without the sweep
// whenever waitDone fired first, which silently orphaned any descendant
// the leader had backgrounded and left running -- exactly the bug
// process-group signaling exists to prevent. (The production analogue,
// internal/sandboxagent/supervisor.Process.Stop, has the identical
// unconditional-sweep fix for its own leader-already-exited-at-entry case,
// but as of this writing still has this same hole open for its
// leader-exits-during-grace case -- see its `case <-p.doneCh: return nil`
// branch -- and is worth fixing the same way in a follow-up.)
//
// waitDone must be closed exactly once, by a single background goroutine
// that has already called (or is about to call) cmd.Wait() -- exec.Cmd.
// Wait must never be called twice, so stopProcessGroup itself never calls
// it; it only ever signals the process group and waits for that one
// goroutine's own signal.
//
// grace should be timeouts.SupervisorShutdownTimeout (internal/platform/
// timeouts.go), NOT the shorter, per-process ProcessStopGracePeriod --
// this mirrors main.go's own StopAll call, which waits up to
// SupervisorShutdownTimeout for ALL of ITS OWN supervised children to
// finish, not just one. Waiting only ProcessStopGracePeriod here would
// risk SIGKILLing the process while ITS OWN graceful shutdown (which may
// itself be running a StopAll across several children) is still
// legitimately in progress -- do not shrink this toward
// ProcessStopGracePeriod, or toward zero, in a future "optimization"; that
// would silently reopen the exact orphan leak this function exists to
// close.
func stopProcessGroup(pgid int, waitDone <-chan struct{}, grace time.Duration) {
	_ = signalProcessGroup(pgid, syscall.SIGTERM)

	select {
	case <-waitDone:
	case <-time.After(grace):
	}

	// Always sweep the whole group, whichever branch fired above -- see
	// the doc comment. If waitDone already fired, this simply blocks on an
	// already-closed channel and returns immediately.
	_ = signalProcessGroup(pgid, syscall.SIGKILL)
	<-waitDone
}

// signalProcessGroup sends sig to the entire process group led by pgid
// (POSIX killpg semantics: the negative of the leader's pid). Mirrors
// internal/sandboxagent/supervisor's own unexported signalGroup exactly,
// duplicated rather than imported -- that helper is unexported from a
// different package, and this test intentionally spawns its target
// process directly rather than going through Supervisor at all (see
// push_integration_test.go's own top doc comment for why: os.Executable()
// must resolve to the real binary, not this test binary). ESRCH (no such
// process -- it already exited) is a benign no-op, never an error worth
// failing a cleanup path over.
func signalProcessGroup(pgid int, sig syscall.Signal) error {
	if err := syscall.Kill(-pgid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// spawnTestProcessGroup starts /bin/sh -c script as the leader of a brand
// new process group (SysProcAttr{Setpgid: true}, exactly runSandboxAgent's
// own convention in push_integration_test.go), returning its pid and a
// channel closed once its single background cmd.Wait() call returns --
// the same shape stopProcessGroup itself expects. A fake, fast stand-in
// (matching internal/sandboxagent/supervisor/supervisor_test.go's own
// spawnShell precedent) rather than the real sandbox-agent binary, since
// what TestStopProcessGroup_KillsCooperativeAndStubbornDescendants below
// exercises is stopProcessGroup's own signal-escalation mechanism, not
// sandbox-agent itself.
func spawnTestProcessGroup(t *testing.T, script string) (pid int, waitDone <-chan struct{}) {
	t.Helper()

	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start test process group: %v", err)
	}

	done := make(chan struct{})
	// errgroup.Group.Go, not a bare `go` statement: §11's no-naked-
	// goroutine rule (tools/lint/narvichecks/nakedgoroutine) applies to
	// tests too -- mirrors internal/sandboxagent/supervisor.Supervisor's
	// own `group` field precedent exactly (Spawn's reap goroutine): this
	// local Group exists solely as a lint-satisfying Go() call site, never
	// Wait()ed on -- done, closed from inside the goroutine, is this
	// function's own actual synchronization signal.
	var group errgroup.Group
	group.Go(func() error {
		_ = cmd.Wait()
		close(done)
		return nil
	})

	return cmd.Process.Pid, done
}

// processGroupMemberAlive reports whether pid currently identifies a live
// process, using the POSIX convention of signal 0 (no actual signal sent,
// only existence/permission checked) -- mirrors internal/sandboxagent/
// supervisor/supervisor_test.go's own identical processAlive helper
// (unexported in a different package, so duplicated here rather than
// imported).
func processGroupMemberAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// waitForTestChildPID polls pidFile until it contains a non-empty pid,
// failing the test if it never appears -- mirrors internal/sandboxagent/
// supervisor/supervisor_test.go's own identical waitForChildPID helper.
func waitForTestChildPID(t *testing.T, pidFile string) int {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(pidFile)
		if err == nil {
			trimmed := strings.TrimSpace(string(raw))
			if trimmed != "" {
				pid, convErr := strconv.Atoi(trimmed)
				if convErr == nil {
					return pid
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("child pid file %s never populated", pidFile)
	return 0
}

// TestStopProcessGroup_KillsCooperativeAndStubbornDescendants is the
// regression test for this Step's own fix: runSandboxAgent's t.Cleanup
// used to call only cmd.Process.Kill() (an unconditional SIGKILL of the
// sandbox-agent process itself, no process group involved) -- SIGKILL
// cannot be intercepted, so sandbox-agent never got a chance to run its
// own already-correct graceful shutdown (main.go's signal.NotifyContext ->
// sup.StopAll), which is what actually stops whatever it had spawned.
// stopProcessGroup replaces that with the same graceful-then-forceful,
// whole-process-group approach internal/sandboxagent/supervisor.Process.
// Stop already uses for each of ITS OWN supervised children.
//
// This spawns a fake stand-in "leader" script (SysProcAttr{Setpgid: true},
// runSandboxAgent's own convention) that itself ignores SIGTERM and
// backgrounds two descendants staying in the SAME process group as the
// leader (a plain `sh -c '...' &` background job under a non-interactive
// shell does not get its own new group unless explicitly setpgid'd) -- one
// COOPERATIVE (exits immediately on SIGTERM) and one STUBBORN (also
// ignores SIGTERM, survives until SIGKILL) -- then proves stopProcessGroup
// leaves NEITHER the leader NOR either descendant alive.
//
// The stubborn descendant (and the deliberately-stubborn leader) is what
// actually proves the process-group SIGKILL escalation fires and reaches
// the whole group: a cooperative-only test would still pass even with the
// SIGKILL escalation deleted entirely, since a cooperative leader dying
// from the initial SIGTERM alone would make stopProcessGroup return before
// ever reaching that code path.
//
// This is deliberately a DIFFERENT mechanism than internal/sandboxagent/
// supervisor/supervisor_test.go's own TestSupervisor_StopAll: that test
// holds direct *supervisor.Process handles (an unexported pgid field, same
// package) and asserts on them directly. This test has no such handle --
// stopProcessGroup drives a real, separately-started *exec.Cmd the exact
// way runSandboxAgent does -- so it scans for the descendants' own
// liveness via their own recorded pids (waitForTestChildPID/
// processGroupMemberAlive) instead of a held Process handle.
func TestStopProcessGroup_KillsCooperativeAndStubbornDescendants(t *testing.T) {
	t.Parallel()

	cooperativePIDFile := filepath.Join(t.TempDir(), "cooperative-pid")
	stubbornPIDFile := filepath.Join(t.TempDir(), "stubborn-pid")

	// The leader itself also ignores TERM (mirroring supervisor_test.go's
	// own TestStop_ForcefulEscalation/TestSupervisor_StopAll precedent).
	// stopProcessGroup now sweeps the whole group with SIGKILL
	// unconditionally, whether waitDone or the grace timer fires first
	// (see its own doc comment), so correctness no longer depends on a
	// race between the leader's own exit and its descendant's -- both
	// orderings converge on the same SIGKILL sweep. A stubborn leader is
	// kept anyway: it usually forces stopProcessGroup's grace timer to
	// actually expire (rather than returning early via waitDone the
	// instant the leader dies to the initial SIGTERM), which is worth
	// exercising even though it is no longer load-bearing for the test's
	// pass/fail outcome.
	//
	// Each backgrounded descendant writes its OWN pid file itself (via its
	// own $$), as its very first action AFTER its own `trap` statement has
	// already run -- deliberately NOT the leader capturing `$!` right
	// after starting it in the background. `$!` is available the instant
	// the job is backgrounded, well before the new process has actually
	// reached (let alone executed) its own `trap` command; polling for a
	// pid file written that way would let this test race ahead and send
	// SIGTERM before the descendant's TERM-ignoring trap is even
	// installed, killing it via the default (terminate) disposition
	// instead of proving the trap -- and the SIGKILL escalation --
	// actually work. Writing the pid file from inside, after `trap`, makes
	// its mere existence proof that the trap is already active.
	script := fmt.Sprintf(
		`sh -c 'trap "exit 0" TERM; echo $$ > %s; while true; do :; done' &
sh -c 'trap "" TERM; echo $$ > %s; while true; do :; done' &
trap '' TERM
while true; do :; done`,
		cooperativePIDFile, stubbornPIDFile,
	)

	leaderPID, waitDone := spawnTestProcessGroup(t, script)

	cooperativePID := waitForTestChildPID(t, cooperativePIDFile)
	stubbornPID := waitForTestChildPID(t, stubbornPIDFile)

	pids := map[string]int{"leader": leaderPID, "cooperative": cooperativePID, "stubborn": stubbornPID}
	for name, pid := range pids {
		if !processGroupMemberAlive(pid) {
			t.Fatalf("%s pid %d not alive before stopProcessGroup()", name, pid)
		}
	}

	const shortGrace = 300 * time.Millisecond // _test.go is exempt from notimeliteral

	start := time.Now()
	stopProcessGroup(leaderPID, waitDone, shortGrace)
	elapsed := time.Since(start)

	// stopProcessGroup returning at all (rather than the test timing out)
	// already proves the SIGKILL escalation fired and unblocked it; the
	// bound below is a generous sanity check, well above the actual OS
	// signal delivery latency.
	if elapsed >= 5*time.Second {
		t.Errorf("stopProcessGroup() took %v, want well under 5s", elapsed)
	}

	// Poll rather than assert once. processGroupMemberAlive is kill(pid, 0),
	// which SUCCEEDS for a zombie -- and SIGKILLing a descendant leaves
	// exactly that for a moment: the leader is already gone, so the
	// descendant has been reparented to init, and it stays a zombie until
	// init reaps it. A single check immediately after stopProcessGroup()
	// returns therefore races the reaper. It happened to pass on darwin and
	// failed on the linux runner, which is the wrong way round for a test
	// whose whole job is to prove descendants do not survive.
	//
	// This does NOT weaken the assertion: without the SIGKILL escalation the
	// stubborn child (trap '' TERM) survives indefinitely, so it is still
	// alive long past this deadline and the test still fails -- which is
	// exactly what the mutation check confirms.
	// Each pid gets its OWN 5s budget, not one deadline shared across the
	// (randomly ordered) map iteration -- a single shared deadline would
	// let an earlier pid's reap latency silently eat into a later pid's
	// allotted time, understating how long that later pid was actually
	// given to disappear.
	for name, pid := range pids {
		deadline := time.Now().Add(5 * time.Second) // _test.go is exempt from notimeliteral
		for processGroupMemberAlive(pid) && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if processGroupMemberAlive(pid) {
			t.Errorf("%s pid %d still alive after stopProcessGroup() -- descendant orphaned", name, pid)
		}
	}
}
