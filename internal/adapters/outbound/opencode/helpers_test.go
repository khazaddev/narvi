package opencode

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/sandboxagent/opencodeproc"
	"github.com/khazaddev/narvi/internal/sandboxagent/supervisor"
)

// White-box (`package opencode`, not opencode_test) so these tests can
// reach unexported members directly — resolveSession, deriveOutcome,
// decodeToolInput, etc. — the same convention
// internal/adapters/outbound/modal's own test files already use.

const (
	// testReadinessTimeout is generous (well above platform.Timeouts.
	// OpenCodeReadinessTimeout's own 30s production default) for the same
	// reason testSSEInactivityTimeout below is: a real dev machine (or a
	// shared, smaller-CPU-count CI runner) running `go test ./...` under
	// -race has many other packages' own test binaries competing for CPU
	// concurrently. 60s was observed to still occasionally time out on
	// GitHub Actions' own hosted runners specifically (confirmed via a
	// from-scratch Docker repro matching the runner's exact node/npm
	// versions and architecture: opencode serve became healthy in under 5s
	// with NO other load competing for CPU, so the timeout, not a broken
	// install, is what's marginal under real CI contention) -- 150s gives
	// real headroom above that without being unbounded.
	testReadinessTimeout      = 150 * time.Second
	testReadinessPollInterval = 250 * time.Millisecond
	// testSSEInactivityTimeout is deliberately generous (well above
	// platform.Timeouts' own 120s production default would need to be
	// scaled down to for a fast test) -- too SHORT a value here was
	// observed to cause spurious "no output" failures under `go test
	// ./...`'s own full-suite concurrency (many -race-instrumented
	// packages compiling/running at once starves this package's own SSE-
	// reading goroutine long enough that the fallback fires before the
	// live model turn has actually finished, not because it produced no
	// output). This only bounds how long a test WAITS before giving up on
	// a genuinely stuck stream, so a generous value costs nothing in the
	// success case.
	testSSEInactivityTimeout = 45 * time.Second
	testWait                 = 15 * time.Second
	testSessionID            = "5b1c1e2e-6b1a-4b1a-9b1a-6b1a4b1a9b1a"

	// testReconnectInterval/testRequestTimeout are the two New() params
	// Batch 5's own audit remediation added (Findings 2/3). These
	// real-opencode-binary-backed tests (startServer/newAdapter below)
	// never deliberately drop the SSE connection or stall a request, so
	// their exact values don't matter for correctness here -- chosen
	// simply as sane, non-degenerate defaults matching each field's own
	// production-default order of magnitude (platform.Timeouts.
	// OpenCodeSSEReconnectInterval/OpenCodeRequestTimeout).
	testReconnectInterval = 2 * time.Second
	testRequestTimeout    = 30 * time.Second
)

// startServer spawns a REAL `opencode serve` process — via
// internal/sandboxagent/opencodeproc, the EXACT same code path
// cmd/sandbox-agent/main.go itself uses — for the duration of one test,
// returning its base URL. Stopped automatically via t.Cleanup.
func startServer(t *testing.T) string {
	t.Helper()

	sup := supervisor.New()
	ctx, cancel := context.WithTimeout(context.Background(), testReadinessTimeout)
	defer cancel()

	// Registered BEFORE the fallible Spawn call below, deliberately: even
	// when Spawn's own readiness wait times out, sup.Spawn (called
	// internally by opencodeproc.Spawn) already registered the OS process
	// with sup the moment it started, well before any readiness check --
	// so sup still knows about it and can still reap it. t.Fatalf calls
	// runtime.Goexit() immediately; a t.Cleanup registered AFTER an
	// `if err != nil { t.Fatalf(...) }` check would never run at all on
	// that path, leaking a real orphaned OS process across test runs.
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), testReadinessTimeout)
		defer stopCancel()
		_ = sup.StopAll(stopCtx, testReadinessPollInterval)
	})

	result, err := opencodeproc.Spawn(ctx, sup, t.TempDir(), testReadinessTimeout, testReadinessPollInterval)
	if err != nil {
		t.Fatalf("opencodeproc.Spawn() error = %v (is the real opencode binary on PATH?)", err)
	}

	return result.BaseURL
}

// newAdapter builds an Adapter against a freshly-started real server,
// stopping its persistent SSE loop via t.Cleanup.
func newAdapter(t *testing.T) *Adapter {
	t.Helper()
	a := New(startServer(t), testSSEInactivityTimeout, testReconnectInterval, testRequestTimeout)
	t.Cleanup(a.Close)
	return a
}

// realProviderConfigured reports whether a real AI-provider credential is
// configured on this machine, WITHOUT ever reading any credential's own
// contents — only whether OpenCode's own credential store
// (~/.local/share/opencode/auth.json, honoring $XDG_DATA_HOME) exists and
// is larger than an empty JSON object, plus two well-known provider env
// vars as a fallback for environments that configure credentials that way
// instead. Tests needing a REAL scripted AI turn call skipIfNoProvider,
// which uses this, to decide whether to run for real or skip gracefully.
func realProviderConfigured() bool {
	if os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("ANTHROPIC_API_KEY") != "" {
		return true
	}

	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		dataHome = filepath.Join(home, ".local", "share")
	}

	info, err := os.Stat(filepath.Join(dataHome, "opencode", "auth.json"))
	if err != nil {
		return false
	}
	return info.Size() > int64(len("{}"))
}

func skipIfNoProvider(t *testing.T) {
	t.Helper()
	if !realProviderConfigured() {
		t.Skip("no AI provider configured (checked OPENAI_API_KEY/ANTHROPIC_API_KEY and " +
			"~/.local/share/opencode/auth.json) -- skipping real-scripted-turn test")
	}
}

// eventCollector is a goroutine-safe ports.EventSink that records every
// AgentEvent handed to it, in order -- shared by every real-scripted-turn
// test below. Goroutine-safety matters here because a turn's own
// execution_complete can be emitted from either the background persistent
// SSE loop (the common path, via session.idle) or the foreground
// StartTurn-calling goroutine itself (the SSE-inactivity fallback path),
// while every other event always comes from the SSE loop.
type eventCollector struct {
	mu     sync.Mutex
	events []ports.AgentEvent
}

func (c *eventCollector) sink(e ports.AgentEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *eventCollector) snapshot() []ports.AgentEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ports.AgentEvent, len(c.events))
	copy(out, c.events)
	return out
}
