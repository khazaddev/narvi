//go:build integration

package sessionactor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/narvidev/narvi/contracts/gen/go/sandboxws"
	narvipg "github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/platform"
)

// This file proves §33.3's control-plane half of the sandbox boot-timing
// relay (recordBootTiming, boottiming.go): a real boot_timing sandbox-ws
// event, driven through the real handleSandboxEvent entry point (exactly
// like every other test in this package -- see opsmetrics_integration_
// test.go's own top comment), genuinely lands in the matching
// sandbox_agent_*_duration_seconds histogram, and -- this is the Step's
// own stated exit criterion -- a forced §6.1 reconnect replay (the SAME
// raw bytes delivered twice) leaves that histogram holding its data point
// EXACTLY once, never twice.

// bootTimingRaw marshals a real, schema-valid sandboxws.BootTiming wire
// payload, mirroring pushpr_integration_test.go's own executionCompleteRaw
// precedent -- but ALSO returns the freshly-minted messageId, unlike that
// precedent: appendRawEvent's own (session_id, messageID) dedup upsert
// (actor.go) keys on SandboxEvent.MessageID, a top-level command field
// wshub peeks from the raw wire bytes in production BEFORE ever
// constructing the command -- it is NOT re-derived from cmd.Raw's own
// embedded "messageId" field. Every call site below must set
// SandboxEvent.MessageID to this SAME returned value, or every event this
// test sends collides on the zero-value "" dedup key and each one after
// the first is silently treated as a redelivery of the one before it.
// tags lets each call site set only the fields its own metric carries
// (events.schema.json's own BootTiming def: every property past "metric"/
// "seconds" is optional, and only a strict subset applies per metric).
func bootTimingRaw(t *testing.T, sessionID string, gen int, metric sandboxws.BootTimingMetric, seconds float64, tags sandboxws.BootTiming) (json.RawMessage, string) {
	t.Helper()
	messageID := uuid.NewString()
	evt := tags
	evt.Type = "boot_timing"
	evt.MessageId = messageID
	evt.SessionId = sessionID
	evt.Gen = gen
	evt.Metric = metric
	evt.Seconds = seconds
	raw, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal boot_timing: %v", err)
	}
	return raw, messageID
}

// readHistogramCountByAttr mirrors readCounterSumByAttr's own precedent
// (opsmetrics_integration_test.go), generalized to a Float64Histogram: sums
// Count only across data points carrying attrValue for attrKey -- used
// below to confirm an attribute this Step's own recordBootTiming sets
// (boot_mode/hook/workspace_moved/failed/degraded), and, just as
// importantly, to confirm "repo" was NEVER recorded as an attribute at all
// (§33.3 point 3).
func readHistogramCountByAttr(ctx context.Context, t *testing.T, reader *sdkmetric.ManualReader, name, attrKey, attrValue string) uint64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != meterName {
			continue
		}
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("%s metric data = %T, want metricdata.Histogram[float64]", name, m.Data)
			}
			var total uint64
			for _, dp := range hist.DataPoints {
				if v, ok := dp.Attributes.Value(attribute.Key(attrKey)); ok && v.AsString() == attrValue {
					total += dp.Count
				}
			}
			return total
		}
	}
	return 0
}

// readHistogramCountByBoolAttr mirrors readHistogramCountByAttr exactly,
// for a BOOL-kind attribute (degraded/failed/workspace_moved) -- a plain
// attribute.Value's own AsString() only returns a meaningful result for a
// STRING-kind value (its own doc comment: "Make sure that the Value's type
// is STRING"), so a bool tag needs AsBool() instead.
func readHistogramCountByBoolAttr(ctx context.Context, t *testing.T, reader *sdkmetric.ManualReader, name, attrKey string, attrValue bool) uint64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != meterName {
			continue
		}
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("%s metric data = %T, want metricdata.Histogram[float64]", name, m.Data)
			}
			var total uint64
			for _, dp := range hist.DataPoints {
				if v, ok := dp.Attributes.Value(attribute.Key(attrKey)); ok && v.AsBool() == attrValue {
					total += dp.Count
				}
			}
			return total
		}
	}
	return 0
}

// histogramCarriesAttrKey reports whether ANY data point for name carries
// attrKey at all, regardless of value -- readHistogramCountByAttr's own
// complement, used to assert an attribute is ABSENT (never set), not just
// that no data point happens to match a particular value.
func histogramCarriesAttrKey(ctx context.Context, t *testing.T, reader *sdkmetric.ManualReader, name, attrKey string) bool {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != meterName {
			continue
		}
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("%s metric data = %T, want metricdata.Histogram[float64]", name, m.Data)
			}
			for _, dp := range hist.DataPoints {
				if _, ok := dp.Attributes.Value(attribute.Key(attrKey)); ok {
					return true
				}
			}
		}
	}
	return false
}

// TestHandleSandboxEvent_BootTiming_RecordsEachOfTheFourHistograms drives
// one real boot_timing event per metric through handleSandboxEvent and
// confirms each lands in its own matching histogram, carrying that
// metric's own tags -- and that "repo" never becomes a metric attribute on
// ANY of the four, even though the wire event itself carries it for three
// of them (§33.3 point 3).
func TestHandleSandboxEvent_BootTiming_RecordsEachOfTheFourHistograms(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	sandboxStore := narvipg.NewSandboxStore(pool)
	if _, err := sandboxStore.Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", nil, nil, "", nil, false)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })
	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	const repo = "repo-boot-timing-attrs-test"
	const uniqueBootMode = "test-boot-mode-boot-timing-relay-unique"

	bootModeBoot := uniqueBootMode
	failedFalse := false
	bootBefore := readHistogramCount(ctx, t, otelReader, "sandbox_agent_boot_duration_seconds")
	bootRaw, bootMsgID := bootTimingRaw(t, sessionID.String(), 1, sandboxws.BootTimingMetricBootDuration, 12.5, sandboxws.BootTiming{
		BootMode: &bootModeBoot,
		Failed:   &failedFalse,
	})
	outcome := sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type:      "boot_timing",
		Gen:       1,
		MessageID: bootMsgID,
		Raw:       bootRaw,
	})
	if !outcome.Persisted {
		t.Error("boot_duration: outcome.Persisted = false, want true")
	}
	waitUntil(t, 5*time.Second, func() bool {
		return readHistogramCount(ctx, t, otelReader, "sandbox_agent_boot_duration_seconds") > bootBefore
	})
	if got := readHistogramCountByAttr(ctx, t, otelReader, "sandbox_agent_boot_duration_seconds", "boot_mode", uniqueBootMode); got != 1 {
		t.Errorf("sandbox_agent_boot_duration_seconds count for boot_mode=%q = %d, want 1", uniqueBootMode, got)
	}

	repoHook := repo
	hookName := "setup.sh"
	bootModeHook := "repo_image"
	workspaceMovedTrue := true
	hookBefore := readHistogramCount(ctx, t, otelReader, "sandbox_agent_hook_rerun_duration_seconds")
	hookRaw, hookMsgID := bootTimingRaw(t, sessionID.String(), 1, sandboxws.BootTimingMetricHookRerunDuration, 1.5, sandboxws.BootTiming{
		Repo:           &repoHook,
		Hook:           &hookName,
		BootMode:       &bootModeHook,
		WorkspaceMoved: &workspaceMovedTrue,
		Failed:         &failedFalse,
	})
	outcome = sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type:      "boot_timing",
		Gen:       1,
		MessageID: hookMsgID,
		Raw:       hookRaw,
	})
	if !outcome.Persisted {
		t.Error("hook_rerun_duration: outcome.Persisted = false, want true")
	}
	waitUntil(t, 5*time.Second, func() bool {
		return readHistogramCount(ctx, t, otelReader, "sandbox_agent_hook_rerun_duration_seconds") > hookBefore
	})
	if got := readHistogramCountByAttr(ctx, t, otelReader, "sandbox_agent_hook_rerun_duration_seconds", "hook", "setup.sh"); got == 0 {
		t.Error("sandbox_agent_hook_rerun_duration_seconds: no data point found for hook=setup.sh")
	}

	repoFetch := repo
	degradedTrue := true
	fetchBefore := readHistogramCount(ctx, t, otelReader, "sandbox_agent_git_fetch_duration_seconds")
	fetchRaw, fetchMsgID := bootTimingRaw(t, sessionID.String(), 1, sandboxws.BootTimingMetricGitFetchDuration, 0.75, sandboxws.BootTiming{
		Repo:     &repoFetch,
		Degraded: &degradedTrue,
	})
	outcome = sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type:      "boot_timing",
		Gen:       1,
		MessageID: fetchMsgID,
		Raw:       fetchRaw,
	})
	if !outcome.Persisted {
		t.Error("git_fetch_duration: outcome.Persisted = false, want true")
	}
	waitUntil(t, 5*time.Second, func() bool {
		return readHistogramCount(ctx, t, otelReader, "sandbox_agent_git_fetch_duration_seconds") > fetchBefore
	})
	if got := readHistogramCountByBoolAttr(ctx, t, otelReader, "sandbox_agent_git_fetch_duration_seconds", "degraded", true); got == 0 {
		t.Error("sandbox_agent_git_fetch_duration_seconds: no data point found for degraded=true")
	}

	repoCheckout := repo
	checkoutBefore := readHistogramCount(ctx, t, otelReader, "sandbox_agent_git_checkout_duration_seconds")
	checkoutRaw, checkoutMsgID := bootTimingRaw(t, sessionID.String(), 1, sandboxws.BootTimingMetricGitCheckoutDuration, 0.3, sandboxws.BootTiming{
		Repo:   &repoCheckout,
		Failed: &failedFalse,
	})
	outcome = sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type:      "boot_timing",
		Gen:       1,
		MessageID: checkoutMsgID,
		Raw:       checkoutRaw,
	})
	if !outcome.Persisted {
		t.Error("git_checkout_duration: outcome.Persisted = false, want true")
	}
	waitUntil(t, 5*time.Second, func() bool {
		return readHistogramCount(ctx, t, otelReader, "sandbox_agent_git_checkout_duration_seconds") > checkoutBefore
	})
	if got := readHistogramCountByBoolAttr(ctx, t, otelReader, "sandbox_agent_git_checkout_duration_seconds", "failed", false); got == 0 {
		t.Error("sandbox_agent_git_checkout_duration_seconds: no data point found for failed=false")
	}

	// §33.3 point 3: "repo" rode all three of the wire events above (hook_
	// rerun/git_fetch/git_checkout each carried it), but must NEVER surface
	// as a metric attribute on ANY of the four histograms -- unbounded
	// cardinality.
	for _, name := range []string{
		"sandbox_agent_boot_duration_seconds",
		"sandbox_agent_hook_rerun_duration_seconds",
		"sandbox_agent_git_fetch_duration_seconds",
		"sandbox_agent_git_checkout_duration_seconds",
	} {
		if histogramCarriesAttrKey(ctx, t, otelReader, name, "repo") {
			t.Errorf("%s carries a 'repo' attribute -- §33.3 point 3 requires repo to ride the event log only, never a metric attribute", name)
		}
	}
}

// TestHandleSandboxEvent_RedeliveredBootTiming_RecordsHistogramOnce is this
// Step's own stated exit criterion: "a test driving a forced WS reconnect
// replay must show each histogram received its data point exactly once."
// Mirrors TestHandleSandboxEvent_RedeliveredLateExecutionComplete_
// RecordsFalseFailureOnce's own exact shape (opsmetrics_integration_
// test.go) -- the SAME raw bytes (same messageId), sent through
// handleSandboxEvent TWICE, reproducing a real §6.1 ack-protocol resend of
// a not-yet-acked best-effort event before reconnect (boot_timing carries
// no ackId at all, so it is exactly the class of event §6.1's own buffered
// resend targets). Removing recordBootTiming's own `inserted` gate (this
// Step's own fix, mirroring turn_false_failure_total's identical
// precedent) makes this test fail: it would instead observe the histogram
// count moving by 2, not 1.
func TestHandleSandboxEvent_RedeliveredBootTiming_RecordsHistogramOnce(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	sandboxStore := narvipg.NewSandboxStore(pool)
	if _, err := sandboxStore.Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", nil, nil, "", nil, false)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })
	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	before := readHistogramCount(ctx, t, otelReader, "sandbox_agent_boot_duration_seconds")

	bootMode := "test-boot-mode-redelivery-unique"
	failed := false
	// Minted ONCE, reused verbatim (both raw bytes AND messageID) for both
	// deliveries below -- a fresh bootTimingRaw call per send would mint a
	// DIFFERENT messageId, and so a genuinely distinct event instead of a
	// redelivery of this one (matches executionCompleteRaw's own identical
	// "same messageID" note, pushpr_integration_test.go). messageID is
	// threaded onto SandboxEvent.MessageID explicitly below -- see
	// bootTimingRaw's own doc comment for why appendRawEvent's dedup upsert
	// needs this SEPARATE top-level field, not cmd.Raw's embedded one.
	raw, messageID := bootTimingRaw(t, sessionID.String(), 1, sandboxws.BootTimingMetricBootDuration, 42.0, sandboxws.BootTiming{
		BootMode: &bootMode,
		Failed:   &failed,
	})

	firstOutcome := sendSandboxEventForTest(ctx, t, a, SandboxEvent{Type: "boot_timing", Gen: 1, MessageID: messageID, Raw: raw})
	if !firstOutcome.Persisted {
		t.Error("first delivery: outcome.Persisted = false, want true")
	}
	waitUntil(t, 5*time.Second, func() bool {
		return readHistogramCount(ctx, t, otelReader, "sandbox_agent_boot_duration_seconds") > before
	})

	// Forced WS reconnect replay: the identical raw bytes AND identical
	// messageID, redelivered -- exactly what a real sandbox connection
	// re-sends, unacked, after a reconnect (§6.1).
	secondOutcome := sendSandboxEventForTest(ctx, t, a, SandboxEvent{Type: "boot_timing", Gen: 1, MessageID: messageID, Raw: raw})
	if !secondOutcome.Persisted {
		t.Error("redelivery: outcome.Persisted = false, want true (still persisted -- appendRawEvent's own upsert always succeeds)")
	}

	// No extra wait needed: sendSandboxEventForTest only returns once
	// cmd.Reply has fired, and handleSandboxEvent's own boot_timing case
	// runs synchronously inside the SAME transact that reply follows --
	// see recordBootTiming's own doc comment (boottiming.go). By the time
	// secondOutcome is in hand, whatever this redelivery did (or correctly
	// did not do) to the histogram has already happened.
	after := readHistogramCountByAttr(ctx, t, otelReader, "sandbox_agent_boot_duration_seconds", "boot_mode", bootMode)
	if after != 1 {
		t.Errorf("sandbox_agent_boot_duration_seconds count for boot_mode=%q = %d, want exactly 1 (delivered the same boot_timing event twice; a redelivery of an already-recorded data point must never double-count)", bootMode, after)
	}
}
