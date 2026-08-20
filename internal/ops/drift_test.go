package ops

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// TestNoMetricDrift is §5.3's own CI-enforcing structural guard
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
//
// Phase 6 audit fix (Finding 3): docs/ONCALL.md used to hand-maintain its
// OWN copy of the alert/symptom -> runbook mapping docs/runbooks/README.md
// already carries, with a comment instructing an editor to keep both
// tables in lockstep -- exactly the shape that let ONCALL.md's own copy
// drift silently (a "Rotating the webhook-signing key" row pointed at the
// cloud-identity OIDC runbook, which never mentions webhooks at all).
// docs/runbooks/README.md is now the ONE surviving table; docs/ONCALL.md
// links to it instead of re-stating it. This extension makes "every
// runbook is reachable from the surviving table" a CI-enforced structural
// guarantee rather than a hand-kept promise: every "*.md" file directly
// inside docs/runbooks EXCEPT README.md itself must be linked from
// README.md's own table exactly once -- zero times means an orphaned
// runbook the index forgot; more than once means a duplicate row.
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

	runbooksDir := filepath.Join(root, "docs", "runbooks")
	entries, err := os.ReadDir(runbooksDir)
	if err != nil {
		t.Fatalf("read dir %s: %v", runbooksDir, err)
	}
	indexRaw, err := os.ReadFile(filepath.Join(runbooksDir, "README.md"))
	if err != nil {
		t.Fatalf("read docs/runbooks/README.md: %v", err)
	}
	// Only markdown TABLE rows count as "the surviving table" -- a
	// filename mentioned in ordinary prose elsewhere in README.md (its
	// own "Cross-references" section already names a few by name, in
	// prose, deliberately not the table) is not what this check is
	// about.
	var tableLines []string
	for _, line := range strings.Split(string(indexRaw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "|") {
			tableLines = append(tableLines, line)
		}
	}
	table := strings.Join(tableLines, "\n")

	var checked int
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" || e.Name() == "README.md" {
			continue
		}
		checked++
		target := "](" + e.Name() + ")"
		count := strings.Count(table, target)
		if count != 1 {
			t.Errorf("docs/runbooks/README.md's own table must link %q exactly once; found %d", e.Name(), count)
		}
	}
	if checked == 0 {
		t.Fatal("found zero non-README runbook files under docs/runbooks -- almost certainly a scan-path bug, not a genuinely empty directory")
	}
}

// TestNoRunbookMetricDrift is the Phase 6 audit's own fix for Finding 2:
// docs/runbooks/README.md and docs/SLOS.md both claimed every metric name
// mentioned in docs/runbooks/*.md is checked against registered OTel
// instruments by this package's own drift test -- false, since CheckDrift
// (drift.go) only ever reads deploy/observability/*.json, never a single
// markdown file. This test makes that claim TRUE for every fenced
// ("```json narvi-metrics```", docmetrics.go) name, mirroring
// TestNoGuideDrift's (guidedrift_test.go) own "narvi-command" precedent
// applied to a "narvi-metrics" block instead.
//
// Mutation-tested by hand as part of this fix's own verification (see the
// PR description for the exact mutations and revert): (1) renaming a real
// instrument's string literal in source (e.g. session_rollout_refused_total
// in internal/adapters/inbound/httpapi/rolloutgate.go) without touching
// any fenced block makes this test fail, since ScanRegisteredInstruments'
// own registered set no longer contains the old name a fenced block still
// cites; (2) adding a ```json narvi-metrics``` block naming a nonexistent
// metric to a real runbook file makes this test fail identically. Both
// mutations were reverted byte-identical afterward.
func TestNoRunbookMetricDrift(t *testing.T) {
	root := repoRoot(t)

	registered, err := ScanRegisteredInstruments(
		filepath.Join(root, "internal"),
		filepath.Join(root, "cmd"),
	)
	if err != nil {
		t.Fatalf("ScanRegisteredInstruments: %v", err)
	}
	if len(registered) == 0 {
		t.Fatal("ScanRegisteredInstruments found zero instruments -- almost certainly a scan-path bug, not a genuinely empty repo")
	}

	runbookClaims, err := LoadRunbookMetrics(filepath.Join(root, "docs", "runbooks"))
	if err != nil {
		t.Fatalf("LoadRunbookMetrics: %v", err)
	}
	if len(runbookClaims) == 0 {
		t.Fatal("LoadRunbookMetrics found zero narvi-metrics blocks -- almost certainly a scan-path bug (several real runbooks name real metrics), not a genuinely empty set")
	}

	sloClaims, err := LoadDocMetrics(filepath.Join(root, "docs", "SLOS.md"))
	if err != nil {
		t.Fatalf("LoadDocMetrics: %v", err)
	}
	if len(sloClaims) == 0 {
		t.Fatal("LoadDocMetrics found zero narvi-metrics blocks in docs/SLOS.md -- almost certainly a scan-path bug, not a genuinely empty set")
	}

	claims := append(append([]MetricsClaim{}, runbookClaims...), sloClaims...)
	if errs := CheckMetricsClaimDrift(claims, registered); len(errs) > 0 {
		for _, e := range errs {
			t.Error(e)
		}
	}
}
