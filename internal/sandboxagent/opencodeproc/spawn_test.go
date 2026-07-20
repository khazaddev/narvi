package opencodeproc_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/khazaddev/narvi/internal/sandboxagent/opencodeproc"
	"github.com/khazaddev/narvi/internal/sandboxagent/supervisor"
)

// These tests need only the OpenCode SERVER itself (spawning it, hitting
// /api/health) -- no AI provider call is ever made, so they run
// unconditionally, no skip needed (starting the server costs nothing and
// needs no credentials).

func TestSpawn_RealBinary(t *testing.T) {
	t.Parallel()

	sup := supervisor.New()
	// A generous 150s test-local readiness bound (well above
	// platform.Timeouts.OpenCodeReadinessTimeout's own 30s production
	// default) -- a real dev machine (or a shared, smaller-CPU-count CI
	// runner) running `go test ./...` under -race has many OTHER packages'
	// own test binaries compiling/running concurrently, which was observed
	// to occasionally starve a fresh opencode serve process past even a
	// 60s bound specifically on GitHub Actions' own hosted runners
	// (confirmed via a from-scratch Docker repro matching the runner's
	// exact node/npm versions and architecture: opencode serve became
	// healthy in under 5s with no other load competing for CPU) --
	// production spawns exactly one opencode server per sandbox VM with no
	// such competition.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	// Registered BEFORE the fallible Spawn call below, deliberately: even
	// when Spawn's own readiness wait times out, its own internal
	// sup.Spawn call already registered the OS process with sup the
	// moment it started, well before any readiness check -- so sup still
	// knows about it and can still reap it. t.Fatalf calls
	// runtime.Goexit() immediately; a t.Cleanup registered AFTER an
	// `if err != nil { t.Fatalf(...) }` check would never run at all on
	// that path, leaking a real orphaned OS process across test runs.
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		_ = sup.StopAll(stopCtx, 5*time.Second)
	})

	result, err := opencodeproc.Spawn(ctx, sup, t.TempDir(), 150*time.Second, 250*time.Millisecond)
	if err != nil {
		t.Fatalf("Spawn() error = %v, want nil (real opencode binary should be on PATH)", err)
	}

	if !strings.HasPrefix(result.BaseURL, "http://127.0.0.1:") {
		t.Errorf("BaseURL = %q, want an http://127.0.0.1:<port> URL", result.BaseURL)
	}
	if result.Version == "" {
		t.Errorf("Version = %q, want a non-empty version string from the real installed binary", result.Version)
	}
	if result.Process == nil {
		t.Fatal("Process = nil, want a live *supervisor.Process")
	}
	if _, exited := result.Process.Exited(); exited {
		t.Error("Process.Exited() = true immediately after a successful Spawn, want still running")
	}

	resp, err := http.Get(result.BaseURL + "/api/health")
	if err != nil {
		t.Fatalf("GET %s/api/health: %v", result.BaseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /api/health status = %d, want 200", resp.StatusCode)
	}
}

// TestSpawn_BadBinaryPath verifies a nonexistent binary fails cleanly and
// promptly (bounded, not hanging) -- PATH is overridden to a directory
// containing no "opencode" executable at all, so exec.Command's own
// LookPath resolution fails immediately inside supervisor.Spawn, well
// before Spawn's own readiness-wait loop would ever start.
func TestSpawn_BadBinaryPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	sup := supervisor.New()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	_, err := opencodeproc.Spawn(ctx, sup, t.TempDir(), 30*time.Second, 250*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Spawn() error = nil, want a fail-fast error for a nonexistent opencode binary")
	}
	if elapsed > 5*time.Second {
		t.Errorf("Spawn() took %s to fail, want a fast, bounded failure (LookPath fails immediately)", elapsed)
	}
}

// TestSpawn_EnvExcludesSessionConfig proves the real regression this
// Step's env-leak remediation fixes: the spawned `opencode serve` process
// must NOT inherit NARVI_SESSION_CONFIG (the sandbox's own plaintext
// bearer token), while ordinary process environment (PATH, HOME) it
// genuinely needs to run is left untouched. PATH is overridden (same
// technique as TestSpawn_BadBinaryPath) to a tempdir containing a real,
// executable fake "opencode" script that probes its own environment and
// writes what it finds to PROBE_FILE, then exits 1 immediately -- Spawn's
// own waitHealthy treats a prompt pre-healthy exit as a fast, bounded
// failure (TestSpawn_BadBinaryPath's own precedent), so asserting Spawn
// returns an error here is consistent, not a new pattern.
func TestSpawn_EnvExcludesSessionConfig(t *testing.T) {
	// Not t.Parallel(): t.Setenv forbids combining the two.
	binDir := t.TempDir()
	probeFile := filepath.Join(t.TempDir(), "probe")

	script := "#!/bin/sh\n" +
		`printf '%s\n' "${NARVI_SESSION_CONFIG:-ABSENT}" > "$PROBE_FILE"` + "\n" +
		`if [ -n "$PATH" ]; then printf 'PATH_PRESENT\n' >> "$PROBE_FILE"; else printf 'PATH_ABSENT\n' >> "$PROBE_FILE"; fi` + "\n" +
		`if [ -n "$HOME" ]; then printf 'HOME_PRESENT\n' >> "$PROBE_FILE"; else printf 'HOME_ABSENT\n' >> "$PROBE_FILE"; fi` + "\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(binDir, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	t.Setenv("PATH", binDir)
	t.Setenv("PROBE_FILE", probeFile)
	t.Setenv("NARVI_SESSION_CONFIG", "marker-should-not-reach-child")

	sup := supervisor.New()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := opencodeproc.Spawn(ctx, sup, t.TempDir(), 5*time.Second, 50*time.Millisecond)
	if err == nil {
		t.Fatal("Spawn() error = nil, want an error (the fake opencode script exits 1 before ever becoming healthy)")
	}

	got, readErr := os.ReadFile(probeFile)
	if readErr != nil {
		t.Fatalf("read probe file: %v", readErr)
	}
	lines := strings.Split(strings.TrimSpace(string(got)), "\n")
	if len(lines) != 3 {
		t.Fatalf("probe file = %q, want 3 lines", got)
	}
	if lines[0] != "ABSENT" {
		t.Errorf("NARVI_SESSION_CONFIG as seen by the spawned process = %q, want %q (must not be inherited)", lines[0], "ABSENT")
	}
	if lines[1] != "PATH_PRESENT" {
		t.Errorf("PATH as seen by the spawned process = %q, want it present", lines[1])
	}
	if lines[2] != "HOME_PRESENT" {
		t.Errorf("HOME as seen by the spawned process = %q, want it present", lines[2])
	}
}
