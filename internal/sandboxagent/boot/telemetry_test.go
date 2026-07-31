package boot_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/khazaddev/narvi/internal/domain/sandboxboot"
	"github.com/khazaddev/narvi/internal/sandboxagent/boot"
	"github.com/khazaddev/narvi/internal/sandboxagent/supervisor"
)

// otelReader is the SINGLE, GLOBAL ManualReader backing the SINGLE, GLOBAL
// SDK MeterProvider TestMain below registers for this whole test binary --
// mirrors internal/app/imagebuild/builder_integration_test.go's own
// TestMain/otelReader precedent exactly. Registering it in TestMain
// (rather than per-test) is load-bearing here specifically: boot's own
// hookRerunDurationHistogram (telemetry.go) resolves LAZILY via
// sync.OnceValue on its very first call, from WHICHEVER test in this
// package happens to invoke RunHooks/RunBoot first -- TestMain's own setup
// runs before m.Run() ever invokes a single test, so every test in this
// package (regardless of ordering) observes the SAME, already-registered
// MeterProvider by the time that first call happens.
var otelReader *sdkmetric.ManualReader

func TestMain(m *testing.M) {
	otelReader = sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(otelReader))
	otel.SetMeterProvider(mp)

	code := m.Run()

	_ = mp.Shutdown(context.Background())
	os.Exit(code)
}

// sumHookRerunDurationCount sums every data point's own count for the
// sandbox_agent_hook_rerun_duration_seconds histogram -- CUMULATIVE across
// every test in this binary (see TestMain's own doc comment), so callers
// must diff a "before" and "after" reading around their own RunHooks/
// RunBoot call(s), matching internal/app/imagebuild's own
// readFailureStreak precedent exactly.
func sumHookRerunDurationCount(ctx context.Context, t *testing.T) uint64 {
	t.Helper()

	var rm metricdata.ResourceMetrics
	if err := otelReader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}

	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != "narvi/sandboxagent-boot" {
			continue
		}
		for _, m := range sm.Metrics {
			if m.Name != "sandbox_agent_hook_rerun_duration_seconds" {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("sandbox_agent_hook_rerun_duration_seconds metric data = %T, want metricdata.Histogram[float64]", m.Data)
			}
			var total uint64
			for _, dp := range hist.DataPoints {
				total += dp.Count
			}
			return total
		}
	}
	return 0
}

// TestRunHooks_RecordsHookRerunDurationMetric proves §19.5(b): a real hook
// run emits a real data point on the sandbox_agent_hook_rerun_duration_seconds
// OTel histogram.
func TestRunHooks_RecordsHookRerunDurationMetric(t *testing.T) {
	ctx := context.Background()
	before := sumHookRerunDurationCount(ctx, t)

	workspaceDir := t.TempDir()
	marker := filepath.Join(workspaceDir, "marker-setup")
	writeScript(t, filepath.Join(workspaceDir, "repo-a", "setup.sh"), "touch "+marker)

	sup := supervisor.New()
	repos := []boot.RepoInfo{{Name: "repo-a", Primary: true}}

	if err := boot.RunHooks(ctx, sup, workspaceDir, repos, sandboxboot.BootModeFresh, nil,
		5*time.Second, time.Second); err != nil {
		t.Fatalf("RunHooks() error = %v, want nil", err)
	}

	after := sumHookRerunDurationCount(ctx, t)
	if after <= before {
		t.Errorf("sandbox_agent_hook_rerun_duration_seconds count did not increase: before=%d after=%d", before, after)
	}
}

// findHookRerunDurationDataPointForRepo returns the (assumed unique, for
// this test's own purposes) data point whose "repo" attribute equals
// repoName -- so a caller can inspect every OTHER attribute that same data
// point carries, mirroring sumHookRerunDurationCount's own collect-and-scan
// shape exactly.
func findHookRerunDurationDataPointForRepo(ctx context.Context, t *testing.T, repoName string) (metricdata.HistogramDataPoint[float64], bool) {
	t.Helper()

	var rm metricdata.ResourceMetrics
	if err := otelReader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}

	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != "narvi/sandboxagent-boot" {
			continue
		}
		for _, m := range sm.Metrics {
			if m.Name != "sandbox_agent_hook_rerun_duration_seconds" {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("sandbox_agent_hook_rerun_duration_seconds metric data = %T, want metricdata.Histogram[float64]", m.Data)
			}
			for _, dp := range hist.DataPoints {
				if v, ok := dp.Attributes.Value(attribute.Key("repo")); ok && v.AsString() == repoName {
					return dp, true
				}
			}
		}
	}
	return metricdata.HistogramDataPoint[float64]{}, false
}

// TestRunHooks_RecordsHookRerunDurationMetric_CarriesBootModeAndWorkspaceMovedAttributes
// proves the fix for the completeness-vs-plan finding: without boot_mode/
// workspace_moved, a cold BootModeBuild setup.sh run and a warm
// BootModeRepoImage+workspaceMoved rerun land in the SAME bucket set, making
// §19.6/Step 43's own adoption trigger unanswerable from this histogram
// alone. Uses a repo name unique to this test (never used by any sibling
// test in this file) so findHookRerunDurationDataPointForRepo's own
// single-match assumption holds against this binary's shared, cumulative
// global MeterProvider (TestMain's own doc comment).
func TestRunHooks_RecordsHookRerunDurationMetric_CarriesBootModeAndWorkspaceMovedAttributes(t *testing.T) {
	ctx := context.Background()

	workspaceDir := t.TempDir()
	marker := filepath.Join(workspaceDir, "marker-setup-attrs")
	writeScript(t, filepath.Join(workspaceDir, "repo-attrs-test", "setup.sh"), "touch "+marker)

	sup := supervisor.New()
	repos := []boot.RepoInfo{{Name: "repo-attrs-test", Primary: true}}
	workspaceMoved := map[string]bool{"repo-attrs-test": true}

	// repo_image + workspaceMoved:true is the ONE cell EvaluateHook actually
	// reruns setup.sh for non-fatally (§19.4) -- the exact cell this
	// histogram exists to isolate from a cold build-mode run.
	if err := boot.RunHooks(ctx, sup, workspaceDir, repos, sandboxboot.BootModeRepoImage, workspaceMoved,
		5*time.Second, time.Second); err != nil {
		t.Fatalf("RunHooks() error = %v, want nil", err)
	}

	dp, found := findHookRerunDurationDataPointForRepo(ctx, t, "repo-attrs-test")
	if !found {
		t.Fatal("no sandbox_agent_hook_rerun_duration_seconds data point found for repo=repo-attrs-test")
	}

	if v, ok := dp.Attributes.Value(attribute.Key("boot_mode")); !ok || v.AsString() != string(sandboxboot.BootModeRepoImage) {
		t.Errorf("boot_mode attribute = (present=%v, value=%q), want (true, %q)", ok, v.AsString(), string(sandboxboot.BootModeRepoImage))
	}
	if v, ok := dp.Attributes.Value(attribute.Key("workspace_moved")); !ok || !v.AsBool() {
		t.Errorf("workspace_moved attribute = (present=%v, value=%v), want (true, true)", ok, v.AsBool())
	}
	if v, ok := dp.Attributes.Value(attribute.Key("hook")); !ok || v.AsString() != string(sandboxboot.HookSetup) {
		t.Errorf("hook attribute = (present=%v, value=%q), want (true, %q)", ok, v.AsString(), string(sandboxboot.HookSetup))
	}
}

// TestRunHooks_HookRerunDurationHistogram_UsesFineGrainedSubFiveSecondBuckets
// proves the fix for Finding 4 (MEDIUM, audit-remediation batch B7): the
// OTel SDK's own default histogram boundaries (0, 5, 10, 25, ...) have
// NOTHING between 0 and 5 -- since this instrument's own unit is seconds,
// every reasonably-healthy, sub-5-second rerun (§19.4's own explicitly
// expected outcome for a warm-cache setup.sh) would land in one single,
// wide [0, 5) bucket, unable to resolve a gradual, leading-indicator trend
// (e.g. p50 creeping from 1s to 3s to 4.5s over several weeks) that §19.6's
// whole premise depends on catching BEFORE it becomes obviously material.
func TestRunHooks_HookRerunDurationHistogram_UsesFineGrainedSubFiveSecondBuckets(t *testing.T) {
	ctx := context.Background()

	workspaceDir := t.TempDir()
	marker := filepath.Join(workspaceDir, "marker-setup-buckets")
	writeScript(t, filepath.Join(workspaceDir, "repo-buckets-test", "setup.sh"), "touch "+marker)

	sup := supervisor.New()
	repos := []boot.RepoInfo{{Name: "repo-buckets-test", Primary: true}}

	if err := boot.RunHooks(ctx, sup, workspaceDir, repos, sandboxboot.BootModeFresh, nil,
		5*time.Second, time.Second); err != nil {
		t.Fatalf("RunHooks() error = %v, want nil", err)
	}

	dp, found := findHookRerunDurationDataPointForRepo(ctx, t, "repo-buckets-test")
	if !found {
		t.Fatal("no sandbox_agent_hook_rerun_duration_seconds data point found for repo=repo-buckets-test")
	}

	subFiveBoundaries := 0
	for _, b := range dp.Bounds {
		if b > 0 && b < 5 {
			subFiveBoundaries++
		}
	}
	if subFiveBoundaries < 3 {
		t.Errorf("sandbox_agent_hook_rerun_duration_seconds bounds = %v, want at least 3 explicit boundaries strictly between 0 and 5 (fine-grained sub-5s resolution, not the SDK's own default [0,5) catch-all bucket)", dp.Bounds)
	}
}

// findBootDurationDataPointForBootMode mirrors
// findHookRerunDurationDataPointForRepo's own precedent exactly, keyed on
// the "boot_mode" attribute instead of "repo" -- sandbox_agent_boot_duration_seconds
// (Finding 3's own fix) carries no per-repo attribute of its own (it is a
// whole-boot, not a per-repo, measurement).
func findBootDurationDataPointForBootMode(ctx context.Context, t *testing.T, bootMode string) (metricdata.HistogramDataPoint[float64], bool) {
	t.Helper()

	var rm metricdata.ResourceMetrics
	if err := otelReader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}

	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != "narvi/sandboxagent-boot" {
			continue
		}
		for _, m := range sm.Metrics {
			if m.Name != "sandbox_agent_boot_duration_seconds" {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("sandbox_agent_boot_duration_seconds metric data = %T, want metricdata.Histogram[float64]", m.Data)
			}
			for _, dp := range hist.DataPoints {
				if v, ok := dp.Attributes.Value(attribute.Key("boot_mode")); ok && v.AsString() == bootMode {
					return dp, true
				}
			}
		}
	}
	return metricdata.HistogramDataPoint[float64]{}, false
}

// TestRecordBootDuration_RecordsMetricWithBootModeAndFailedAttributes proves
// the fix for Finding 3 (HIGH, audit-remediation batch B7): before this fix,
// nothing anywhere measured total boot-to-ready wall-clock time, so §19.6's
// "is a hook rerun materially eroding the warm-boot latency win" gating
// question had no denominator to compare sandbox_agent_hook_rerun_duration_seconds
// against. RecordBootDuration is cmd/sandbox-agent/main.go's own one call
// site's entry point (runBootSequence's own timing bracket); a distinct,
// test-unique boot_mode string isolates this data point from any other
// test's own recording against this binary's shared, cumulative
// MeterProvider.
func TestRecordBootDuration_RecordsMetricWithBootModeAndFailedAttributes(t *testing.T) {
	ctx := context.Background()
	const uniqueBootMode = "test-boot-mode-metrics-unique"

	boot.RecordBootDuration(ctx, uniqueBootMode, 12.5, false)

	dp, found := findBootDurationDataPointForBootMode(ctx, t, uniqueBootMode)
	if !found {
		t.Fatal("no sandbox_agent_boot_duration_seconds data point found -- total boot-to-ready latency is still unmeasured")
	}
	if dp.Count == 0 {
		t.Error("sandbox_agent_boot_duration_seconds data point has count 0, want at least one recorded observation")
	}
	if v, ok := dp.Attributes.Value(attribute.Key("failed")); !ok || v.AsBool() {
		t.Errorf("failed attribute = (present=%v, value=%v), want (true, false)", ok, v.AsBool())
	}
}

// TestRecordBootDuration_FailedBoot_RecordsFailedAttributeTrue proves the
// failed=true side of the same attribute -- a failed boot's own elapsed
// time is still a real, worth-recording data point (main.go's own call
// site records it unconditionally, regardless of bootErr), not one to
// silently discard.
func TestRecordBootDuration_FailedBoot_RecordsFailedAttributeTrue(t *testing.T) {
	ctx := context.Background()
	const uniqueBootMode = "test-boot-mode-metrics-unique-failed"

	boot.RecordBootDuration(ctx, uniqueBootMode, 3.2, true)

	dp, found := findBootDurationDataPointForBootMode(ctx, t, uniqueBootMode)
	if !found {
		t.Fatal("no sandbox_agent_boot_duration_seconds data point found")
	}
	if v, ok := dp.Attributes.Value(attribute.Key("failed")); !ok || !v.AsBool() {
		t.Errorf("failed attribute = (present=%v, value=%v), want (true, true)", ok, v.AsBool())
	}
}
