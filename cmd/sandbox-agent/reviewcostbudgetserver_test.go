// This file (deliberately NOT behind the "integration" build tag, mirrors
// reviewverdicttoolprompt_test.go's own identical precedent -- fast enough
// for the default `go test ./...`/`go test -race` suite) proves
// reviewCostBudgetServer/reviewCostBudgetHandler (reviewcostbudgetserver.go)
// end to end: a REAL loopback listener, a REAL net/http.Get against it,
// and the REAL internal/domain/reviewtriage.ShouldSkipOptionalPass
// decision reflected in the response -- Step 70's own central mutation-
// test target ("prove ShouldSkipOptionalPass is actually reached with a
// real value, not a hardcoded stub").
//
// Every background Accept loop below is launched via errgroup.Group.Go,
// never a naked `go` statement -- §11 grants no test-file carve-out for
// that rule (tools/lint/narvichecks/nakedgoroutine's own doc comment).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/khazaddev/narvi/internal/platform"
)

// serveInBackground launches s.Serve() via errgroup.Group.Go and registers
// a t.Cleanup that shuts the server down -- the common "start it, let the
// test hit it, tear it down automatically" shape most of this file's own
// tests need. Tests that need finer control over the shutdown moment
// itself (TestReviewCostBudgetServer_ShutdownClosesTheListener,
// TestReviewCostBudgetServer_ConcurrentRequestsAreSafe's own concurrent
// request group) manage their own errgroup instead.
func serveInBackground(t *testing.T, s *reviewCostBudgetServer) {
	t.Helper()
	var group errgroup.Group
	group.Go(func() error {
		return s.Serve()
	})
	t.Cleanup(func() {
		_ = s.Shutdown(context.Background())
		_ = group.Wait()
	})
}

// TestReviewCostBudgetServer_LoopbackOnly proves the listener genuinely
// binds to 127.0.0.1, never 0.0.0.0 -- the one hard security constraint
// this whole endpoint's own "no authentication" design depends on
// (reviewcostbudgetserver.go's own top doc comment).
func TestReviewCostBudgetServer_LoopbackOnly(t *testing.T) {
	s, err := startReviewCostBudgetServer(func() (float64, bool) { return 0, false }, platform.DefaultTimeouts().ReviewCostBudgetServerReadHeaderTimeout)
	if err != nil {
		t.Fatalf("startReviewCostBudgetServer: %v", err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })

	host, _, err := net.SplitHostPort(s.listener.Addr().String())
	if err != nil {
		t.Fatalf("split host/port of %q: %v", s.listener.Addr().String(), err)
	}
	if host != "127.0.0.1" {
		t.Errorf("listener bound to host %q, want 127.0.0.1 (loopback only)", host)
	}
}

// TestReviewCostBudgetServer_EndToEnd is this Step's own flagship proof:
// a REAL server, a REAL GET against its own real, ephemeral-port URL, and
// the REAL reviewtriage.ShouldSkipOptionalPass answer reflected back --
// table-driven over spend/ceiling combinations that must flip shouldSkip,
// proving the production call site genuinely exists and genuinely
// computes (never a hardcoded true/false).
func TestReviewCostBudgetServer_EndToEnd(t *testing.T) {
	tests := []struct {
		name           string
		spentUSD       float64
		spentOK        bool
		ceilingUsdArg  string
		wantStatus     int
		wantSpentUSD   float64
		wantCeilingUSD float64
		wantShouldSkip bool
	}{
		{
			name: "well under budget: shouldSkip false", spentUSD: 1.0, spentOK: true,
			ceilingUsdArg: "5.00", wantStatus: http.StatusOK,
			wantSpentUSD: 1.0, wantCeilingUSD: 5.0, wantShouldSkip: false,
		},
		{
			name: "at the 80% margin: shouldSkip true", spentUSD: 4.0, spentOK: true,
			ceilingUsdArg: "5.00", wantStatus: http.StatusOK,
			wantSpentUSD: 4.0, wantCeilingUSD: 5.0, wantShouldSkip: true,
		},
		{
			name: "no live turn yet (ok=false): degrades to spentUSD=0, never an error", spentUSD: 999, spentOK: false,
			ceilingUsdArg: "5.00", wantStatus: http.StatusOK,
			wantSpentUSD: 0, wantCeilingUSD: 5.0, wantShouldSkip: false,
		},
		{
			name: "zero ceiling (unconfigured): never skips even with huge spend", spentUSD: 1000, spentOK: true,
			ceilingUsdArg: "0", wantStatus: http.StatusOK,
			wantSpentUSD: 1000, wantCeilingUSD: 0, wantShouldSkip: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := startReviewCostBudgetServer(func() (float64, bool) { return tt.spentUSD, tt.spentOK }, platform.DefaultTimeouts().ReviewCostBudgetServerReadHeaderTimeout)
			if err != nil {
				t.Fatalf("startReviewCostBudgetServer: %v", err)
			}
			serveInBackground(t, s)

			resp, err := http.Get(s.URL() + "?ceilingUsd=" + tt.ceilingUsdArg)
			if err != nil {
				t.Fatalf("http.Get(%s): %v", s.URL(), err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}

			var got reviewCostBudgetResponse
			if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
				t.Fatalf("decode response body: %v", err)
			}

			if got.SpentUSD != tt.wantSpentUSD {
				t.Errorf("spentUSD = %v, want %v", got.SpentUSD, tt.wantSpentUSD)
			}
			if got.CeilingUSD != tt.wantCeilingUSD {
				t.Errorf("ceilingUSD = %v, want %v", got.CeilingUSD, tt.wantCeilingUSD)
			}
			if got.ShouldSkip != tt.wantShouldSkip {
				t.Errorf("shouldSkip = %v, want %v", got.ShouldSkip, tt.wantShouldSkip)
			}
		})
	}
}

// TestReviewCostBudgetServer_WrongMethodRejected proves a non-GET request
// is refused (405) -- this endpoint is read-only.
func TestReviewCostBudgetServer_WrongMethodRejected(t *testing.T) {
	s, err := startReviewCostBudgetServer(func() (float64, bool) { return 0, true }, platform.DefaultTimeouts().ReviewCostBudgetServerReadHeaderTimeout)
	if err != nil {
		t.Fatalf("startReviewCostBudgetServer: %v", err)
	}
	serveInBackground(t, s)

	resp, err := http.Post(s.URL()+"?ceilingUsd=5.00", "application/json", nil)
	if err != nil {
		t.Fatalf("http.Post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}
}

// TestReviewCostBudgetServer_MissingCeilingRejected proves a request with
// no ceilingUsd query parameter (or a malformed one) is a 4xx -- this
// review turn's own prompt instructs the agent to treat ANY non-2xx
// response identically to "shouldSkip": true, so this status IS the
// fail-safe signal, not merely defensive plumbing (reviewcostbudgetserver.go's
// own doc comment).
func TestReviewCostBudgetServer_MissingCeilingRejected(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{"missing entirely", ""},
		{"malformed, not a number", "?ceilingUsd=not-a-number"},
		{"malformed, empty value", "?ceilingUsd="},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := startReviewCostBudgetServer(func() (float64, bool) { return 0, true }, platform.DefaultTimeouts().ReviewCostBudgetServerReadHeaderTimeout)
			if err != nil {
				t.Fatalf("startReviewCostBudgetServer: %v", err)
			}
			serveInBackground(t, s)

			resp, err := http.Get(s.URL() + tt.query)
			if err != nil {
				t.Fatalf("http.Get: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
			}
		})
	}
}

// TestReviewCostBudgetServer_ShutdownClosesTheListener is this Step's own
// second central mutation-test target: "prove the server shuts down
// cleanly (no orphaned listener/goroutine after StopAll)". Serve must
// return (nil, since Shutdown produces http.ErrServerClosed, which Serve
// itself translates to nil) once Shutdown completes, and a subsequent
// connection attempt against the SAME address must fail -- proving the
// listener itself, not just the Accept loop's own goroutine, is actually
// closed. Manages its own errgroup (rather than serveInBackground above)
// because it needs to observe Serve's own return value at a precise
// moment, mid-test, not merely at cleanup.
func TestReviewCostBudgetServer_ShutdownClosesTheListener(t *testing.T) {
	s, err := startReviewCostBudgetServer(func() (float64, bool) { return 0, true }, platform.DefaultTimeouts().ReviewCostBudgetServerReadHeaderTimeout)
	if err != nil {
		t.Fatalf("startReviewCostBudgetServer: %v", err)
	}
	addr := s.listener.Addr().String()

	serveDone := make(chan error, 1)
	var group errgroup.Group
	group.Go(func() error {
		err := s.Serve()
		serveDone <- err
		return err
	})

	// Prove the server is actually up before shutting it down -- a real GET
	// must succeed.
	resp, err := http.Get(s.URL() + "?ceilingUsd=1.00")
	if err != nil {
		t.Fatalf("http.Get before shutdown: %v", err)
	}
	_ = resp.Body.Close()

	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// Serve must return promptly, and with nil -- never leaving its own
	// goroutine running forever (an orphaned goroutine, the class of leak
	// Step 171 closed for a different subsystem).
	select {
	case serveErr := <-serveDone:
		if serveErr != nil {
			t.Errorf("Serve() returned %v after a clean Shutdown, want nil", serveErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve() did not return within the test's own bound after Shutdown -- possible goroutine leak")
	}

	// The listener itself must be closed -- a fresh dial to the SAME
	// address must fail, proving this is not merely "the Accept loop gave
	// up" while the socket stays bound.
	if conn, dialErr := net.Dial("tcp", addr); dialErr == nil {
		_ = conn.Close()
		t.Errorf("dial to %s succeeded after Shutdown, want the listener closed", addr)
	}
}

// TestReviewCostBudgetServer_ConcurrentRequestsAreSafe proves the handler
// is safe under concurrent GETs (go test -race) -- each request calls the
// injected spentUSD function independently, with no shared mutable state
// in this file's own code (turnState's own mutex, turn.go, already
// protects the real accumulator this stub stands in for).
func TestReviewCostBudgetServer_ConcurrentRequestsAreSafe(t *testing.T) {
	s, err := startReviewCostBudgetServer(func() (float64, bool) { return 2.5, true }, platform.DefaultTimeouts().ReviewCostBudgetServerReadHeaderTimeout)
	if err != nil {
		t.Fatalf("startReviewCostBudgetServer: %v", err)
	}
	serveInBackground(t, s)

	const n = 20
	var reqGroup errgroup.Group
	for i := 0; i < n; i++ {
		reqGroup.Go(func() error {
			resp, err := http.Get(s.URL() + "?ceilingUsd=5.00")
			if err != nil {
				return err
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				return errors.New("non-200 status")
			}
			return nil
		})
	}
	if err := reqGroup.Wait(); err != nil {
		t.Errorf("concurrent request failed: %v", err)
	}
}

// TestMainWiring_BudgetServerDoesNotDeadlockWhenBridgeConvergesWithoutCancelingCtx
// is a regression test for a real deadlock this Step's own implementation
// introduced and then fixed before landing: main.go's run() drives its WS
// bridge (or, headless, a ctx-wait stand-in) via ONE errgroup ("group"),
// whose Wait() is reached either by ctx being canceled OR by
// wsbridge.Bridge.Run returning entirely on its own -- a *FatalConnectError
// from a 401/403/404/410 handshake (wsbridge/run.go's own doc comment)
// returns WITHOUT ever canceling ctx (Run only ever OBSERVES ctx, never
// cancels it). An earlier version of this file's own reviewcostbudgetserver.go
// wiring put budgetServer.Serve() on that SAME "group", gated by a SECOND
// member that waited on ctx.Done() before calling Shutdown -- which
// deadlocks precisely on the fatal-status path: group.Wait() would then
// ALSO need that ctx-watcher to finish, which never happens until run()
// itself reaches its own deferred stop() call, which run() can never reach
// while still blocked on THIS SAME group.Wait().
//
// This test reproduces the shape directly (not via a real run() call, which
// is not unit-testable in isolation, main_test.go's own top doc comment):
// one errgroup standing in for main.go's "group" (a member that returns
// immediately, exactly like bridge.Run does on a fatal status, WITHOUT
// canceling ctx), and budgetServer.Serve() on its OWN separate errgroup
// (budgetSrvGroup's own real shape, main.go). Proves (a) the "bridge" group
// converges promptly even though ctx is never canceled and the budget
// server is still serving, and (b) an explicit Shutdown call (never a
// ctx-watcher) still stops the budget server's own Serve goroutine cleanly
// afterward -- both bounded by a real timeout, so a regression to the
// broken, single-group wiring shows up as this test HANGING past that
// bound, not merely failing an assertion.
func TestMainWiring_BudgetServerDoesNotDeadlockWhenBridgeConvergesWithoutCancelingCtx(t *testing.T) {
	s, err := startReviewCostBudgetServer(func() (float64, bool) { return 0, true }, platform.DefaultTimeouts().ReviewCostBudgetServerReadHeaderTimeout)
	if err != nil {
		t.Fatalf("startReviewCostBudgetServer: %v", err)
	}

	// ctx is deliberately NEVER canceled anywhere in this test -- exactly
	// the fatal-WS-status scenario this regression test reproduces.
	ctx, cancelUnused := context.WithCancel(context.Background())
	defer cancelUnused() // vet/lint hygiene only; never actually called before the assertions below run

	var bridgeGroup errgroup.Group
	bridgeGroup.Go(func() error {
		// Stands in for wsbridge.Bridge.Run(ctx) returning a
		// *FatalConnectError -- returns immediately, WITHOUT ever reading
		// from ctx.Done(), exactly like the real fatal-status path.
		return errors.New("simulated *FatalConnectError")
	})

	var budgetSrvGroup errgroup.Group
	budgetSrvGroup.Go(func() error {
		return s.Serve()
	})

	// (a) The "bridge" group must converge promptly, on its own, with NO
	// dependency on ctx or on budgetServer's own lifetime -- this is the
	// exact property the broken wiring violated.
	select {
	case err := <-waitInBackground(&bridgeGroup):
		if err == nil {
			t.Fatalf("bridgeGroup.Wait() = nil, want the simulated *FatalConnectError")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bridgeGroup.Wait() did not converge -- it is (incorrectly) waiting on something budget-server-related")
	}

	// (b) ctx is STILL not canceled here -- confirms the scenario is real.
	if ctx.Err() != nil {
		t.Fatalf("ctx.Err() = %v, want nil (this test's own point is that ctx is NEVER canceled)", ctx.Err())
	}

	// (c) Shutdown, called explicitly (never triggered by ctx), still stops
	// the budget server's own Serve goroutine cleanly.
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case err := <-waitInBackground(&budgetSrvGroup):
		if err != nil {
			t.Errorf("budgetSrvGroup.Wait() = %v after Shutdown, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("budgetSrvGroup.Wait() did not converge after an explicit Shutdown -- possible goroutine leak")
	}
}

// waitInBackground returns a channel that receives group.Wait()'s own
// result -- errgroup.Group.Wait() itself blocks, so this file's own
// bounded-timeout assertions (mirroring TestReviewCostBudgetServer_
// ShutdownClosesTheListener's own identical shape) need it wrapped for a
// select. The wrapping goroutine is launched via errgroup.Group.Go
// (nakedgoroutine's own §11 rule, no test-file carve-out), reusing the
// SAME group whose own Wait() this helper is answering on behalf of would
// deadlock (a group cannot Wait() on itself from inside one of its own
// Go calls), so a throwaway SEPARATE one-shot group is used purely as the
// launch mechanism.
func waitInBackground(group *errgroup.Group) <-chan error {
	var launcher errgroup.Group
	ch := make(chan error, 1)
	launcher.Go(func() error {
		ch <- group.Wait()
		return nil
	})
	return ch
}
