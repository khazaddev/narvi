// This file proves shutdownControlPlaneOTel's own bounded-wait contract in
// isolation -- serve() itself is not unit-testable (it blocks on OS
// signals, a real Postgres pool, and a real chi-routed HTTP server), but
// this seam alone is exactly the piece §33 changes: serve()'s own deferred
// OTel shutdown used to run unbounded (platform.SetupOTel's own comment,
// cmd/sandbox-agent/main.go's shutdownSandboxAgentOTel doc comment), which
// was safe only as long as the flush it bounded was a bare stdout write.
// Mirrors cmd/sandbox-agent/main_test.go's own
// TestShutdownSandboxAgentOTel_BoundsAHungShutdown/
// _FastShutdown_ReturnsPromptly pair exactly, one binary over.
package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestShutdownControlPlaneOTel_BoundsAHungShutdown proves the fix §33
// requires directly: serve()'s own deferred shutdownOTel call previously
// had no timeout of its own (see this func's own doc comment), so a hang
// in an OTLP exporter's own flush against a down/unreachable collector
// could block this long-running daemon's process exit indefinitely.
// hungShutdown below never returns on its own unless its OWN ctx is
// canceled -- exactly modeling that hang -- so this test proves
// shutdownControlPlaneOTel itself supplies the missing bound: it must
// return within (comfortably less than) the real-world hang duration
// hungShutdown would otherwise wait for, and the returned error must be
// the timeout's own context.DeadlineExceeded, not nil.
func TestShutdownControlPlaneOTel_BoundsAHungShutdown(t *testing.T) {
	hungShutdown := func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Hour): // stands in for an unboundedly-blocked flush
			return nil
		}
	}

	const timeout = 50 * time.Millisecond
	start := time.Now()
	err := shutdownControlPlaneOTel(hungShutdown, timeout)
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("shutdownControlPlaneOTel() error = %v, want context.DeadlineExceeded", err)
	}
	// Generous upper bound (20x the configured timeout) so this test is not
	// flaky under CI scheduling jitter, while still failing hard if the
	// call actually waited anywhere near hungShutdown's own 1-hour branch --
	// proving the timeout was genuinely applied, not merely accepted as a
	// parameter and ignored.
	if elapsed > 20*timeout {
		t.Errorf("shutdownControlPlaneOTel() took %s, want it bounded near timeout=%s (proves serve()'s own process exit is no longer unbounded)", elapsed, timeout)
	}
}

// TestShutdownControlPlaneOTel_FastShutdown_ReturnsPromptly proves the
// ordinary, non-hung path: a shutdown func that returns immediately must
// not be made to wait out the full timeout -- shutdownControlPlaneOTel only
// BOUNDS the wait, it does not introduce one of its own.
func TestShutdownControlPlaneOTel_FastShutdown_ReturnsPromptly(t *testing.T) {
	start := time.Now()
	err := shutdownControlPlaneOTel(func(context.Context) error { return nil }, time.Hour)
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("shutdownControlPlaneOTel() error = %v, want nil", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("shutdownControlPlaneOTel() took %s for an already-fast shutdown func, want it to return promptly rather than waiting on anything", elapsed)
	}
}

// fakeAppPermissionsChecker is a minimal appPermissionsChecker fake --
// there is no real GitHub App reachable from this environment (see
// internal/adapters/outbound/githubapp's own doc.go), so
// verifyGitHubAppScopeAtBoot is tested against this instead of a real
// githubapp.Client.
type fakeAppPermissionsChecker struct {
	permissions map[string]string
	err         error
}

func (f fakeAppPermissionsChecker) AppPermissions(context.Context) (map[string]string, error) {
	return f.permissions, f.err
}

// TestVerifyGitHubAppScopeAtBoot_ReadOnlySucceeds proves an App whose own
// granted permissions are read-only lets boot proceed (nil error).
func TestVerifyGitHubAppScopeAtBoot_ReadOnlySucceeds(t *testing.T) {
	checker := fakeAppPermissionsChecker{permissions: map[string]string{"contents": "read", "metadata": "read"}}
	if err := verifyGitHubAppScopeAtBoot(context.Background(), checker, time.Second); err != nil {
		t.Errorf("verifyGitHubAppScopeAtBoot() error = %v, want nil", err)
	}
}

// TestVerifyGitHubAppScopeAtBoot_BroadPermissionsRefusesToStart is §30.4(4)'s
// own named test: "a boot with a broad credential in the shadow slot
// refuses to start, loudly." An operator who pastes/configures a GitHub
// App with (e.g.) Contents: Read & write must never boot silently into a
// state where every shadow sandbox is re-armed with a write-capable
// credential on the first mint.
func TestVerifyGitHubAppScopeAtBoot_BroadPermissionsRefusesToStart(t *testing.T) {
	checker := fakeAppPermissionsChecker{permissions: map[string]string{"contents": "write", "metadata": "read"}}
	err := verifyGitHubAppScopeAtBoot(context.Background(), checker, time.Second)
	if err == nil {
		t.Fatal("verifyGitHubAppScopeAtBoot() error = nil, want a boot refusal for a write-capable App")
	}
	if strings.Contains(err.Error(), "§") {
		t.Errorf("verifyGitHubAppScopeAtBoot() error = %q, must not cite an internal section number in operator-facing text", err.Error())
	}
}

// TestVerifyGitHubAppScopeAtBoot_IntrospectionFailureRefusesToStart proves
// a genuine failure to even ASK GitHub for the App's own permissions
// (network error, invalid credentials, ...) also refuses to boot -- an
// unknown scope must never be treated as an acceptable one.
func TestVerifyGitHubAppScopeAtBoot_IntrospectionFailureRefusesToStart(t *testing.T) {
	checker := fakeAppPermissionsChecker{err: errors.New("simulated network failure")}
	if err := verifyGitHubAppScopeAtBoot(context.Background(), checker, time.Second); err == nil {
		t.Fatal("verifyGitHubAppScopeAtBoot() error = nil, want a boot refusal when the App's own scope cannot even be determined")
	}
}
