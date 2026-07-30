package boot_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
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
