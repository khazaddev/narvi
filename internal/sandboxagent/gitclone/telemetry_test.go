package gitclone_test

import (
	"context"
	"path/filepath"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
	"github.com/khazaddev/narvi/internal/domain/gitstate"
	"github.com/khazaddev/narvi/internal/sandboxagent/gitclone"
	"github.com/khazaddev/narvi/internal/sandboxagent/supervisor"
)

// findHistogramDataPointForRepo mirrors internal/sandboxagent/boot/
// telemetry_test.go's own findHookRerunDurationDataPointForRepo precedent
// exactly: this whole test binary shares ONE, cumulative, global
// MeterProvider/ManualReader (otelReader, registered in clone_test.go's own
// TestMain) across every test in this package, so a caller must use a repo
// name unique to its own test (never reused by any sibling test in this
// file/package) to find a single, unambiguous data point.
func findHistogramDataPointForRepo(ctx context.Context, t *testing.T, metricName, repoName string) (metricdata.HistogramDataPoint[float64], bool) {
	t.Helper()

	var rm metricdata.ResourceMetrics
	if err := otelReader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}

	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != "narvi/sandboxagent-gitclone" {
			continue
		}
		for _, m := range sm.Metrics {
			if m.Name != metricName {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("%s metric data = %T, want metricdata.Histogram[float64]", metricName, m.Data)
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

// TestSyncAll_RecordsGitFetchAndCheckoutDurationMetrics proves the fix for
// Finding 3 (HIGH, audit-remediation batch B7): before this fix, nothing in
// this package (or the codebase at large) measured the §19.3 boot-time
// fetch step's own added latency, nor SyncAll's own checkout time -- both
// entirely untimed, leaving §19.6's "materially eroding warm-boot latency"
// gating question with no denominator/comparison metric for either
// contributing phase, only a bare hook-rerun number with nothing to weigh
// it against.
//
// No "origin" remote is configured for this repo at all (initRepo's own
// plain, remote-less fixture) -- repo.Branch is nil (an invented
// "narvi/<sessionID>" branch), so the fetch step is expected to degrade
// (§19.3's own "acceptable from HEAD" case) rather than fail this repo
// outright, and the subsequent checkout onto HEAD is expected to succeed.
// Deliberately NOT t.Parallel() (unlike most of this file's other tests):
// this test's own repo name must be the ONLY one of its kind recorded
// against this binary's shared, cumulative MeterProvider by the time it
// collects, and keeping it serial removes any doubt about interleaving
// with a concurrent SyncAll call using the identical name.
func TestSyncAll_RecordsGitFetchAndCheckoutDurationMetrics(t *testing.T) {
	ctx := context.Background()

	workspaceDir := t.TempDir()
	repoName := "repo-fetch-checkout-metrics"
	repoDir := filepath.Join(workspaceDir, repoName)
	initRepo(t, repoDir) // branch "main", one commit, no "origin" configured

	sessionID := "session-fetch-checkout-metrics"
	repos := []sessionconfig.SessionConfigReposElem{
		{Name: repoName, Url: "https://example.invalid/repo1.git"},
	}

	sup := supervisor.New()
	results, err := gitclone.SyncAll(ctx, sup, workspaceDir, repos, nil, sessionID,
		testFetchStepTimeout, testSyncStepTimeout, testStopGrace, func(string, string, string) {})
	if err != nil {
		t.Fatalf("SyncAll() error = %v, want nil", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Err != nil {
		t.Fatalf("results[0].Err = %v, want nil", results[0].Err)
	}
	if results[0].State != gitstate.StateReady {
		t.Fatalf("results[0].State = %s, want ready", results[0].State)
	}

	fetchDP, found := findHistogramDataPointForRepo(ctx, t, "sandbox_agent_git_fetch_duration_seconds", repoName)
	if !found {
		t.Fatal("no sandbox_agent_git_fetch_duration_seconds data point found -- the §19.3 fetch step's own latency is still untimed")
	}
	if fetchDP.Count == 0 {
		t.Error("sandbox_agent_git_fetch_duration_seconds data point has count 0, want at least one recorded observation")
	}
	if v, ok := fetchDP.Attributes.Value(attribute.Key("degraded")); !ok || !v.AsBool() {
		t.Errorf("degraded attribute = (present=%v, value=%v), want (true, true) -- no real 'origin' remote is configured, so this repo's own fetch is expected to degrade", ok, v.AsBool())
	}

	checkoutDP, found := findHistogramDataPointForRepo(ctx, t, "sandbox_agent_git_checkout_duration_seconds", repoName)
	if !found {
		t.Fatal("no sandbox_agent_git_checkout_duration_seconds data point found -- SyncAll's own checkout time is still untimed")
	}
	if checkoutDP.Count == 0 {
		t.Error("sandbox_agent_git_checkout_duration_seconds data point has count 0, want at least one recorded observation")
	}
	if v, ok := checkoutDP.Attributes.Value(attribute.Key("failed")); !ok || v.AsBool() {
		t.Errorf("failed attribute = (present=%v, value=%v), want (true, false) -- this repo's own checkout succeeded", ok, v.AsBool())
	}
}
