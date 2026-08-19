package ops

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// repoRoot locates this repo's own root directory relative to this test
// file's own location (internal/ops/drift_test.go -> ../.. -> repo root)
// via runtime.Caller, so this test works regardless of the working
// directory `go test` happens to be invoked from.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// TestNoMetricDrift is Step 77's own CI-enforcing structural guard
// (doc.go): scans this repo's REAL internal/ and cmd/ Go source for every
// registered OTel instrument, loads the REAL
// deploy/observability/{dashboards,alerts} files, and fails if either
// references a metric name the code does not actually register. This
// runs as a plain `go test`, already part of `make test`/
// `go test -race ./...` (a required step in .github/workflows/ci.yml's
// "checks" job) -- no separate CI job or workflow edit needed for this
// check to gate every PR.
//
// Mutation-tested by hand as part of this Step's own verification (see
// the PR description): (1) adding a panel referencing a nonexistent
// metric to a real dashboard file makes this test fail; (2) renaming a
// real instrument's string literal in source (without touching the
// dashboard/alert files) makes this test fail identically, since
// ScanRegisteredInstruments' own registered set no longer contains the
// old name. Both mutations were reverted byte-identical afterward.
func TestNoMetricDrift(t *testing.T) {
	root := repoRoot(t)

	registered, err := ScanRegisteredInstruments(
		filepath.Join(root, "internal"),
		filepath.Join(root, "cmd"),
	)
	if err != nil {
		t.Fatalf("ScanRegisteredInstruments: %v", err)
	}
	if len(registered) == 0 {
		t.Fatal("ScanRegisteredInstruments found zero instruments -- almost certainly a scan-path bug (this repo has real OTel instruments), not a genuinely empty repo")
	}

	dashboards, err := LoadDashboards(filepath.Join(root, "deploy", "observability", "dashboards"))
	if err != nil {
		t.Fatalf("LoadDashboards: %v", err)
	}
	if len(dashboards) == 0 {
		t.Fatal("LoadDashboards found zero dashboards")
	}

	alerts, err := LoadAlerts(filepath.Join(root, "deploy", "observability", "alerts"))
	if err != nil {
		t.Fatalf("LoadAlerts: %v", err)
	}
	if len(alerts) == 0 {
		t.Fatal("LoadAlerts found zero alerts")
	}

	if errs := CheckDrift(dashboards, alerts, registered); len(errs) > 0 {
		for _, e := range errs {
			t.Error(e)
		}
	}
}

// TestAlertRunbooksExist is a cheap companion check, same rationale as
// TestNoMetricDrift: an alert pointing an operator at a runbook that does
// not exist on disk is a smaller version of the identical "looks
// maintained but isn't" hazard.
func TestAlertRunbooksExist(t *testing.T) {
	root := repoRoot(t)
	alerts, err := LoadAlerts(filepath.Join(root, "deploy", "observability", "alerts"))
	if err != nil {
		t.Fatalf("LoadAlerts: %v", err)
	}
	for _, a := range alerts {
		if a.Runbook == "" {
			continue
		}
		p := filepath.Join(root, a.Runbook)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("alert %q: runbook %q does not exist: %v", a.Name, a.Runbook, err)
		}
	}
}
