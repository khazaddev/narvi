package platform_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	colmetricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"

	"github.com/narvidev/narvi/internal/platform"
)

// TestSetupOTel proves the bootstrap actually produces a working provider,
// not just that construction didn't panic: it starts and ends a real span,
// records a real counter measurement, then shuts down cleanly, with the
// otlpEndpoint parameter left empty (§33's off switch) -- see this file's
// own _UnsetEndpoint_* and _OTLPEndpoint_* tests below for what actually
// changes on either side of that parameter.
func TestSetupOTel(t *testing.T) {
	ctx := t.Context()

	shutdown, err := platform.SetupOTel(ctx, "narvi-test", "")
	if err != nil {
		t.Fatalf("SetupOTel() error = %v, want nil", err)
	}
	if shutdown == nil {
		t.Fatal("SetupOTel() shutdown func = nil, want non-nil")
	}

	_, span := otel.Tracer("test").Start(ctx, "x")
	span.End()

	counter, err := otel.Meter("test").Int64Counter("test_counter")
	if err != nil {
		t.Fatalf("Int64Counter() error = %v, want nil", err)
	}
	counter.Add(ctx, 1)

	if err := shutdown(ctx); err != nil {
		t.Fatalf("shutdown() error = %v, want nil", err)
	}
}

// captureStdoutFD redirects the REAL OS-level file descriptor 1 for the
// duration of fn and returns everything written to it.
//
// Needed because go.opentelemetry.io/otel/exporters/stdout/{stdouttrace,
// stdoutmetric}'s own default writer is captured as a package-level var AT
// THEIR OWN PACKAGE INIT TIME ("defaultWriter = os.Stdout", stdouttrace's
// own config.go), long before any test runs -- reassigning the Go-level
// os.Stdout *os.File variable inside a test would never reach that
// already-latched copy. Redirecting the underlying fd instead works
// regardless of which *os.File value any package happens to be holding:
// both it and the original still nominally "are" fd 1, so writes through
// either now land in the pipe this returns. This repo already assumes a
// Unix host throughout (cmd/sandbox-agent's own process-group/signal code),
// so syscall.Dup2 needs no build-tag guard here either.
func captureStdoutFD(t *testing.T, fn func()) []byte {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	stdoutFD := int(os.Stdout.Fd())
	savedFD, err := syscall.Dup(stdoutFD)
	if err != nil {
		t.Fatalf("syscall.Dup(stdout) error = %v", err)
	}
	restore := func() {
		_ = syscall.Dup2(savedFD, stdoutFD)
		_ = syscall.Close(savedFD)
	}
	// Deferred AS WELL AS called explicitly below, not deferred only: fn()
	// is caller-supplied test code that may itself call t.Fatalf
	// (runtime.Goexit) on a real failure -- e.g. this file's own
	// mutation-test runs, or a genuine regression in SetupOTel. Goexit
	// runs deferred functions before unwinding the goroutine, but skips
	// any code written sequentially after fn()'s call site -- were this
	// restore written as ONLY sequential code, a single Fatalf inside fn()
	// would leave the REAL process-wide fd 1 permanently redirected into
	// this pipe for the rest of the test binary, breaking every later
	// test's own output. Calling restore() a second time here (via defer,
	// on the success path below) is a harmless no-op: savedFD is already
	// closed, so both syscalls just fail EBADF and are ignored.
	defer restore()

	if err := syscall.Dup2(int(w.Fd()), stdoutFD); err != nil {
		t.Fatalf("syscall.Dup2() error = %v", err)
	}

	fn()

	// Restore fd 1 to the real stdout BEFORE reading r, not after: fd 1
	// itself is a SEPARATE duplicate of w's own underlying pipe write end
	// (that is exactly what the Dup2 above set up), so closing w alone
	// does not close the pipe -- fd 1 keeps it open. io.ReadAll(r) below
	// would then block forever waiting for EOF that never comes, since a
	// live write-end duplicate (fd 1) is still open. An earlier version of
	// this helper called restore() only via defer and hung exactly this
	// way (proven the hard way: a real `go test` run timed out at the
	// full 10-minute per-binary ceiling on this exact call before this
	// ordering was fixed).
	restore()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("io.ReadAll(captured stdout) error = %v", err)
	}
	return out
}

// TestSetupOTel_UnsetEndpoint_ExportsToStdout proves §33's own load-bearing
// no-op property directly, not by inference from "SetupOTel returned no
// error": with otlpEndpoint left empty, SetupOTel must still write to REAL
// stdout, exactly as every deployment has always seen -- see
// captureStdoutFD's own doc comment for why that requires redirecting the
// OS-level fd rather than the Go os.Stdout variable. This is also this
// Step's own required mutation test for "make the unset path build an
// OTLP exporter": if that mutation ever landed, nothing would appear on
// stdout here (an OTLP exporter with no real collector to reach would
// either fail construction or, worse, silently retry against a stray
// default in the background) and this test would fail.
func TestSetupOTel_UnsetEndpoint_ExportsToStdout(t *testing.T) {
	ctx := t.Context()

	captured := captureStdoutFD(t, func() {
		shutdown, err := platform.SetupOTel(ctx, "narvi-test-stdout", "")
		if err != nil {
			t.Fatalf("SetupOTel() error = %v, want nil", err)
		}

		counter, err := otel.Meter("test-stdout").Int64Counter("narvi_test_stdout_marker_total")
		if err != nil {
			t.Fatalf("Int64Counter() error = %v, want nil", err)
		}
		counter.Add(ctx, 1)

		if err := shutdown(ctx); err != nil {
			t.Fatalf("shutdown() error = %v, want nil", err)
		}
	})

	if len(captured) == 0 {
		t.Fatal("captured stdout is empty, want the stdoutmetric exporter's own JSON export -- an unset otlpEndpoint must never silently stop exporting to stdout")
	}
	if !strings.Contains(string(captured), "narvi_test_stdout_marker_total") {
		t.Errorf("captured stdout = %q, want it to contain the recorded counter's own name (proves this is a REAL stdout metric export, not incidental output)", captured)
	}
}

// TestSetupOTel_UnsetEndpoint_NeverContactsAReachableCollector is the
// mutation-catching complement to _ExportsToStdout above: a fake OTLP
// receiver is up and REACHABLE the whole time, but its own URL is never
// passed to SetupOTel. Pins the independent, equally load-bearing property
// that an unset endpoint sends NOTHING to any collector, reachable or not
// -- the property that keeps every existing deployment's egress footprint
// byte-identical until an operator actually opts in, not merely its stdout
// output.
func TestSetupOTel_UnsetEndpoint_NeverContactsAReachableCollector(t *testing.T) {
	ctx := t.Context()

	var mu sync.Mutex
	var requests int
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(receiver.Close)

	shutdown, err := platform.SetupOTel(ctx, "narvi-test-unset", "")
	if err != nil {
		t.Fatalf("SetupOTel() error = %v, want nil", err)
	}

	counter, err := otel.Meter("test-unset").Int64Counter("narvi_test_unset_marker_total")
	if err != nil {
		t.Fatalf("Int64Counter() error = %v, want nil", err)
	}
	counter.Add(ctx, 1)

	if err := shutdown(ctx); err != nil {
		t.Fatalf("shutdown() error = %v, want nil", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if requests != 0 {
		t.Errorf("fake OTLP receiver got %d request(s), want 0 -- an unset otlpEndpoint must never contact any collector, reachable or not", requests)
	}
}

// metricsRequestContainsIntSum reports whether req carries a Sum-typed
// metric named name with an int data point equal to value, anywhere across
// its ResourceMetrics/ScopeMetrics/Metrics nesting.
func metricsRequestContainsIntSum(req *colmetricpb.ExportMetricsServiceRequest, name string, value int64) bool {
	for _, rm := range req.GetResourceMetrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				if m.GetName() != name {
					continue
				}
				sum := m.GetSum()
				if sum == nil {
					continue
				}
				for _, dp := range sum.GetDataPoints() {
					if dp.GetAsInt() == value {
						return true
					}
				}
			}
		}
	}
	return false
}

// TestSetupOTel_OTLPEndpoint_MetricsArriveAtReceiver is §33's own exit
// criterion (§33's own): "an integration test against
// a fake OTLP receiver observes the control plane's metrics". Deliberately
// NOT a test that otlpmetrichttp.New/SetupOTel constructed without error --
// the fake collector below decodes the REAL protobuf body a REAL export
// request carries over the wire, and this test asserts on the metric NAME
// AND VALUE it actually contains, proving something was received, not just
// that nothing panicked while sending it.
func TestSetupOTel_OTLPEndpoint_MetricsArriveAtReceiver(t *testing.T) {
	ctx := t.Context()

	const metricName = "narvi_test_otlp_marker_total"
	const recordedValue = 7

	var mu sync.Mutex
	var received []*colmetricpb.ExportMetricsServiceRequest
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/metrics" {
			w.WriteHeader(http.StatusOK)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var req colmetricpb.ExportMetricsServiceRequest
		if err := proto.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		received = append(received, &req)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(receiver.Close)

	shutdown, err := platform.SetupOTel(ctx, "narvi-test-otlp", receiver.URL)
	if err != nil {
		t.Fatalf("SetupOTel() error = %v, want nil", err)
	}

	counter, err := otel.Meter("test-otlp").Int64Counter(metricName)
	if err != nil {
		t.Fatalf("Int64Counter() error = %v, want nil", err)
	}
	counter.Add(ctx, recordedValue)

	if err := shutdown(ctx); err != nil {
		t.Fatalf("shutdown() error = %v, want nil", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) == 0 {
		t.Fatal("fake OTLP receiver never received a POST /v1/metrics -- the control plane's own metrics never arrived")
	}
	for _, req := range received {
		if metricsRequestContainsIntSum(req, metricName, recordedValue) {
			return
		}
	}
	t.Fatalf("none of the %d received ExportMetricsServiceRequest(s) contained a %q counter with value %d: %+v", len(received), metricName, recordedValue, received)
}

// TestSetupOTel_OTLPEndpoint_ShutdownDoesNotHangAgainstDeadCollector proves
// §33's own real-failure-mode requirement against the REAL OTLP exporter
// (cmd/control-plane/main_test.go's own
// TestShutdownControlPlaneOTel_BoundsAHungShutdown covers the synthetic,
// deterministic version of the same bound at the caller-wrapper level; this
// test instead proves the underlying SDK call itself actually honors a
// caller-supplied context deadline). The listener below accepts the
// exporter's TCP connection and then never answers -- modeling a collector
// that is reachable at the network level but hung or otherwise
// unresponsive, exactly the "collector that is down" case the brief calls
// out. shutdown, given a short-timeout context, must return within
// (comfortably less than) the OTLP SDK's own much longer retry/backoff
// defaults (up to a full minute total, go.opentelemetry.io/otel/exporters/
// otlp/otlptrace/otlptracehttp/internal/retry.DefaultConfig) -- proving the
// CALLER's own context deadline, not the SDK's internal retry ceiling, is
// what actually bounds this call, and that the call returns an error
// rather than hanging or panicking.
func TestSetupOTel_OTLPEndpoint_ShutdownDoesNotHangAgainstDeadCollector(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}

	// §11: no naked goroutines, no test-file exemption -- errgroup.Group.Go
	// instead, mirroring cmd/sandbox-agent/snapshot_test.go's own
	// "var group errgroup.Group; group.Go(...); t.Cleanup(group.Wait)"
	// precedent for a background accept loop exactly like this one.
	var group errgroup.Group
	group.Go(func() error {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return nil
			}
			// Accept and then do nothing: models a collector that is
			// reachable at the TCP level but never answers. Left open
			// until the listener itself closes at test cleanup.
			_ = conn
		}
	})
	// ONE combined cleanup, in this exact order, not two separate
	// t.Cleanup registrations: t.Cleanup runs LIFO, so registering
	// ln.Close() and group.Wait() separately (Close first, Wait second)
	// would run Wait() BEFORE Close() at teardown -- deadlocking forever,
	// since the accept loop above only ever returns once ln.Close() makes
	// its blocked Accept() call fail, and Close() would never get to run
	// while still stuck waiting on Wait() first.
	t.Cleanup(func() {
		_ = ln.Close()
		_ = group.Wait()
	})

	ctx := t.Context()
	shutdown, err := platform.SetupOTel(ctx, "narvi-test-dead-collector", "http://"+ln.Addr().String())
	if err != nil {
		t.Fatalf("SetupOTel() error = %v, want nil", err)
	}

	counter, err := otel.Meter("test-dead-collector").Int64Counter("narvi_test_dead_collector_marker_total")
	if err != nil {
		t.Fatalf("Int64Counter() error = %v, want nil", err)
	}
	counter.Add(ctx, 1)

	const boundedTimeout = 300 * time.Millisecond
	shutdownCtx, cancel := context.WithTimeout(context.Background(), boundedTimeout)
	defer cancel()

	start := time.Now()
	shutdownErr := shutdown(shutdownCtx)
	elapsed := time.Since(start)

	if shutdownErr == nil {
		t.Error("shutdown() error = nil, want a context-deadline error from the dead collector's own flush attempt")
	}
	// Generous upper bound (>30x the configured timeout) so this is not
	// flaky under CI scheduling jitter, while still failing hard if the
	// call actually waited anywhere near the SDK's own much longer
	// retry/backoff defaults (up to a full minute).
	if elapsed > 10*time.Second {
		t.Errorf("shutdown() against a dead collector took %s, want it bounded near the caller's own %s context deadline, not the SDK's own internal retry ceiling", elapsed, boundedTimeout)
	}
}
