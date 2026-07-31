// This file (deliberately NOT behind the "integration" build tag) proves
// setupSandboxAgentOTel's own real-provider wiring directly, in-process --
// run() itself is not unit-testable in isolation (it blocks on OS signals /
// a live WS bridge / a real opencode spawn), but this seam alone is exactly
// the piece the audit finding this Step fixes is about: "cmd/sandbox-agent
// never calls platform.SetupOTel", so sandbox_agent_hook_rerun_duration_seconds
// (internal/sandboxagent/boot, §19.5(b)) recorded against the no-op global
// MeterProvider every process starts with by default.
package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/khazaddev/narvi/internal/domain/sandboxboot"
	"github.com/khazaddev/narvi/internal/sandboxagent/boot"
)

// TestSetupSandboxAgentOTel_InstallsRealMeterProvider proves the fix
// directly: BEFORE this call, the global MeterProvider is a known no-op
// (explicitly reset here, not just assumed from process start, so this test
// is correct regardless of what any other test in this binary might have
// left registered); AFTER it, otel.GetMeterProvider() is no longer that same
// no-op value -- i.e. a real SDK MeterProvider is now installed globally,
// exactly what boot's own hookRerunDurationHistogram (sync.OnceValue,
// telemetry.go) needs to observe the first time a hook actually runs.
//
// Not t.Parallel(): this test mutates the process-wide global OTel
// MeterProvider/TracerProvider, which no other test in this package touches
// today -- kept non-parallel anyway, defensively, and restores the original
// provider afterward so it can never leak into a later test in this binary.
func TestSetupSandboxAgentOTel_InstallsRealMeterProvider(t *testing.T) {
	original := otel.GetMeterProvider()
	defer otel.SetMeterProvider(original)

	knownNoop := noop.NewMeterProvider()
	otel.SetMeterProvider(knownNoop)

	shutdown, err := setupSandboxAgentOTel(context.Background())
	if err != nil {
		t.Fatalf("setupSandboxAgentOTel() error = %v, want nil", err)
	}
	if shutdown == nil {
		t.Fatal("setupSandboxAgentOTel() shutdown = nil, want non-nil")
	}
	defer func() {
		if err := shutdown(context.Background()); err != nil {
			t.Errorf("shutdown() error = %v, want nil", err)
		}
	}()

	got := otel.GetMeterProvider()
	if got == knownNoop {
		t.Fatal("otel.GetMeterProvider() is still the known no-op provider after setupSandboxAgentOTel() -- sandbox-agent's own hook-rerun-duration histogram would still record into the void")
	}

	// A real, non-no-op meter must actually be usable -- proves this isn't
	// merely "some other value", but a working SDK MeterProvider.
	counter, err := got.Meter("test").Int64Counter("test_counter")
	if err != nil {
		t.Fatalf("Int64Counter() error = %v, want nil", err)
	}
	counter.Add(context.Background(), 1)
}

// TestShutdownSandboxAgentOTel_BoundsAHungShutdown proves the fix for
// Finding 1 (MEDIUM, audit-remediation batch B7): run()'s own deferred
// shutdownOTel call previously had no timeout of its own, so a hang in the
// stdout metric/trace exporter's own flush (a slow/blocked log collector, a
// full pipe buffer under load, ...) could block sandbox-agent's process
// exit indefinitely. hungShutdown below never returns on its own unless its
// OWN ctx is canceled -- exactly modeling that hang -- so this test proves
// shutdownSandboxAgentOTel itself supplies the missing bound: it must
// return within (comfortably less than) the real-world hang duration
// hungShutdown would otherwise wait for, and the returned error must be the
// timeout's own context.DeadlineExceeded, not nil.
func TestShutdownSandboxAgentOTel_BoundsAHungShutdown(t *testing.T) {
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
	err := shutdownSandboxAgentOTel(hungShutdown, timeout)
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("shutdownSandboxAgentOTel() error = %v, want context.DeadlineExceeded", err)
	}
	// Generous upper bound (20x the configured timeout) so this test is not
	// flaky under CI scheduling jitter, while still failing hard if the
	// call actually waited anywhere near hungShutdown's own 1-hour branch --
	// proving the timeout was genuinely applied, not merely accepted as a
	// parameter and ignored.
	if elapsed > 20*timeout {
		t.Errorf("shutdownSandboxAgentOTel() took %s, want it bounded near timeout=%s (proves the process exit is no longer unbounded)", elapsed, timeout)
	}
}

// TestShutdownSandboxAgentOTel_FastShutdown_ReturnsPromptly proves the
// ordinary, non-hung path: a shutdown func that returns immediately must
// not be made to wait out the full timeout -- shutdownSandboxAgentOTel only
// BOUNDS the wait, it does not introduce one of its own.
func TestShutdownSandboxAgentOTel_FastShutdown_ReturnsPromptly(t *testing.T) {
	start := time.Now()
	err := shutdownSandboxAgentOTel(func(context.Context) error { return nil }, time.Hour)
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("shutdownSandboxAgentOTel() error = %v, want nil", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("shutdownSandboxAgentOTel() took %s for an immediately-returning shutdown func, want near-instant", elapsed)
	}
}

// captureLog swaps slog.Default() for a JSON handler writing into a
// *bytes.Buffer for the duration of fn, restoring the original logger
// afterward -- mirrors internal/sandboxagent/gitclone/sync_test.go's own
// identical logger-capture precedent. Not t.Parallel()-safe by construction
// (a global mutation), so every caller here is a plain, non-parallel test.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()

	original := slog.Default()
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(original)

	fn()
	return buf.String()
}

// TestLogImageManifest_ReadFailed proves the (unchanged) manifestErr != nil
// case still logs -- a genuine I/O/parse failure.
func TestLogImageManifest_ReadFailed(t *testing.T) {
	logged := captureLog(t, func() {
		logImageManifest(sandboxboot.BootModeRepoImage, boot.ImageManifest{}, false, errors.New("boom"), nil)
	})
	if !strings.Contains(logged, "read image manifest failed") {
		t.Errorf("log output = %q, want the read-failure warning", logged)
	}
}

// TestLogImageManifest_MissingUnderRepoImage_Warns proves the fix for
// Finding 4(d): a manifest genuinely ABSENT (not unreadable) under
// BootModeRepoImage specifically must still be logged -- previously
// completely silent, even though it flips EVERY repo to workspaceMoved:
// true (ComputeWorkspaceMoved's own safe default) and is exactly as
// consistent with "a build-service bug stopped baking manifests" as with
// "working as designed" for a pre-Step image.
func TestLogImageManifest_MissingUnderRepoImage_Warns(t *testing.T) {
	logged := captureLog(t, func() {
		logImageManifest(sandboxboot.BootModeRepoImage, boot.ImageManifest{}, false, nil, nil)
	})
	if !strings.Contains(logged, "repo_image boot has no image manifest") {
		t.Errorf("log output = %q, want the missing-manifest-under-repo_image warning", logged)
	}
}

// TestLogImageManifest_MissingUnderOtherModes_NoLog proves the routine case
// stays silent: every OTHER boot mode never had an image-build step to bake
// a manifest at all, so a missing manifest there is expected, not worth a
// log line -- unchanged by this Step.
func TestLogImageManifest_MissingUnderOtherModes_NoLog(t *testing.T) {
	for _, mode := range []sandboxboot.BootMode{sandboxboot.BootModeBuild, sandboxboot.BootModeFresh, sandboxboot.BootModeSnapshotRestore} {
		logged := captureLog(t, func() {
			logImageManifest(mode, boot.ImageManifest{}, false, nil, nil)
		})
		if logged != "" {
			t.Errorf("boot_mode=%s: log output = %q, want no log line for a routinely-absent manifest", mode, logged)
		}
	}
}

// TestLogImageManifest_Found_LogsFingerprintAndBuiltRepoShas proves the fix
// for Finding 4(a)/(b)/(c): a found manifest's own Fingerprint/BuiltAt
// (manifest.go's own "diagnostic/log purposes only" doc comment) and
// BuiltRepoShas were never logged anywhere in the binary -- so an operator
// had no way to see which baked image a sandbox was really running, nor
// what built_repo_shas its post-clone SHAs were compared against.
func TestLogImageManifest_Found_LogsFingerprintAndBuiltRepoShas(t *testing.T) {
	manifest := boot.ImageManifest{
		Fingerprint:   "fp-abc123",
		BuiltAt:       time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		BuiltRepoShas: map[string]string{"repo1": "deadbeef"},
	}

	logged := captureLog(t, func() {
		logImageManifest(sandboxboot.BootModeRepoImage, manifest, true, nil, map[string]string{"repo1": "deadbeef"})
	})

	for _, want := range []string{"fp-abc123", "deadbeef", "repo1", "2026-01-02"} {
		if !strings.Contains(logged, want) {
			t.Errorf("log output = %q, want it to contain %q", logged, want)
		}
	}
}

// TestLogImageManifest_RepoMissingFromManifest_LogsDistinctWarning proves
// the fix for Finding 5 (LOW, audit-remediation batch B7): a repo present
// in the post-clone/-sync currentSHAs map but ABSENT as its own key in the
// found manifest's own BuiltRepoShas (e.g. added to the session's repo list
// after the image was last baked) must log its OWN, distinct signal --
// previously folded silently into the same "found" Info line as a repo
// whose SHA simply moved, indistinguishable without manually diffing two
// separate log lines by hand.
//
// "repo-present" (in both maps, different SHAs -- an ordinary SHA move)
// must NOT trigger the new missing-repo warning; only "repo-missing" (a key
// in currentSHAs with no counterpart in BuiltRepoShas at all) must.
func TestLogImageManifest_RepoMissingFromManifest_LogsDistinctWarning(t *testing.T) {
	manifest := boot.ImageManifest{
		Fingerprint:   "fp-xyz",
		BuiltRepoShas: map[string]string{"repo-present": "built-sha"},
	}
	currentSHAs := map[string]string{
		"repo-present": "current-sha", // SHA genuinely moved -- routine, not this fix's own case
		"repo-missing": "some-sha",    // absent from BuiltRepoShas entirely -- this fix's own case
	}

	logged := captureLog(t, func() {
		logImageManifest(sandboxboot.BootModeRepoImage, manifest, true, nil, currentSHAs)
	})

	if !strings.Contains(logged, "repo absent from image manifest") {
		t.Errorf("log output = %q, want a distinct warning naming the repo absent from the manifest's built_repo_shas", logged)
	}
	if !strings.Contains(logged, `"repo":"repo-missing"`) {
		t.Errorf("log output = %q, want the absent-repo warning to name repo-missing specifically", logged)
	}

	// A repo present in BOTH maps (even with a different SHA -- ordinary
	// drift) must NOT get this distinct warning: it is already covered by
	// the routine "image manifest" Info line and hooks.go's own per-repo
	// workspace_moved:true boot_progress signal, which this fix leaves
	// entirely unchanged.
	for _, line := range strings.Split(strings.TrimSpace(logged), "\n") {
		if strings.Contains(line, "repo absent from image manifest") && strings.Contains(line, "repo-present") {
			t.Errorf("log line = %q, want repo-present (present in both maps) to never trigger the absent-repo warning", line)
		}
	}
}
