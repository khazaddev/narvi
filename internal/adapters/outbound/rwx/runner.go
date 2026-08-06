package rwx

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
)

// cliRunner abstracts one `rwx` CLI subprocess invocation -- the seam
// every test in this package (other than realbinary_test.go's one
// deliberately-skipped stub) substitutes a fake for, mirroring how
// modal.Provider's own tests substitute an httptest.Server for Modal's
// real HTTP API one level up the stack: modal fakes the HTTP round trip;
// this fakes the exec.Cmd round trip instead, since RWX's own
// sandbox-lifecycle transport is a CLI, never HTTP (doc.go).
type cliRunner interface {
	// Run executes the pinned `rwx` binary with args, using env as the
	// subprocess's COMPLETE environment (callers that want ambient-env
	// inheritance, e.g. execCLIRunner's own real caller, build env from
	// osEnviron() themselves first -- Run itself never merges anything
	// in). Returns the captured stdout/stderr and the process's own exit
	// code on a clean exit; exitCode is -1 and err is non-nil when the
	// process never completed at all (failed to start, or was killed by
	// ctx's own deadline) -- mirroring internal/sandboxagent/supervisor.
	// Process.Wait's own Result{ExitCode, Err} two-tier distinction
	// exactly (see gitclone.cloneOne's identical "waitErr vs
	// result.ExitCode" precedent): a genuine process-level failure (err)
	// is never confused with an ordinary nonzero exit (exitCode).
	Run(ctx context.Context, args []string, env []string) (stdout, stderr []byte, exitCode int, err error)
}

// execCLIRunner is the real cliRunner: exec.CommandContext against the
// pinned `rwx` binary. Constructed by New; never used in any test in this
// package other than realbinary_test.go's own skipped stub.
type execCLIRunner struct {
	binary string
}

func (r execCLIRunner) Run(ctx context.Context, args []string, env []string) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, r.binary, args...)
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	if runErr == nil {
		return stdout.Bytes(), stderr.Bytes(), 0, nil
	}

	// A genuine, clean nonzero exit -- exitErr.ExitCode() itself returns
	// -1 for a process terminated by a SIGNAL rather than an ordinary
	// exit (Go's own documented ExitCode behavior), which would otherwise
	// collide with this function's own -1 sentinel below for "never
	// completed at all" -- the >= 0 guard keeps the two distinct. A
	// signal-killed process (most commonly: ctx's own deadline, via
	// exec.CommandContext's documented SIGKILL-the-direct-child behavior)
	// is a process-level failure, not an ordinary nonzero exit, so it
	// deliberately falls through to that branch instead.
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) && exitErr.ExitCode() >= 0 {
		return stdout.Bytes(), stderr.Bytes(), exitErr.ExitCode(), nil
	}

	// The process never completed cleanly at all: it failed to start
	// (binary not on PATH, permission denied -- never an *exec.ExitError
	// at all), or it was killed by a signal (caught above). Either way
	// this is a process-level failure, not an ordinary nonzero exit --
	// exitCode -1 signals that distinction to the caller (classifyCLIError,
	// errors.go).
	return stdout.Bytes(), stderr.Bytes(), -1, runErr
}

// osEnviron is a package-level var (never called directly as os.Environ()
// at each use site) purely so tests can override it deterministically --
// mirrors this codebase's own established Clock-injection precedent for
// isolating a real ambient-environment read from a test, and matches
// gitclone.cloneOne's own "inherit the ambient environment" rationale:
// the subprocess needs PATH/HOME/etc. (and, per §4.1.1, any ambient
// HTTPS_PROXY -- "the subprocess inherits proxy env vars... so RWX
// traffic routes like Modal's") alongside the two entries this package
// itself appends (RWX_ACCESS_TOKEN, SESSION_CONFIG).
var osEnviron = os.Environ
