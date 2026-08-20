package ops

import (
	"path/filepath"
	"testing"
)

// TestNoSectionRefDrift fails when any Go comment or plan document cites a
// docs/TECHNICAL_PLAN.md section that does not exist. It runs as a plain
// `go test`, already inside `go test -race ./...` in CI's own "checks" job --
// no new workflow, matching TestNoMetricDrift/TestNoGuideDrift.
//
// It exists because a Phase 6 audit found 239 comment citations using the
// section sigil where "Step N" was meant, across 60 files. Every referent was
// correct; only the sigil was wrong, and that was harmless purely because
// TECHNICAL_PLAN has no section numbered 46, 60 or 62 yet -- see sectionref.go
// for why that is a countdown rather than a stable state. Under this repo's
// convention, code cites only the durable §N.M, never the Step number that
// happened to introduce it -- so the fix is the section, not "Step N".
//
// Mutation-tested as part of this change's own verification: adding a citation
// of a nonexistent section number to a real source comment makes this fail, and
// renaming a real subsection heading in the plan without updating its citations
// fails it the same way. Both were reverted byte-identical.
func TestNoSectionRefDrift(t *testing.T) {
	root := repoRoot(t)

	defined, err := ScanPlanSections(filepath.Join(root, "docs", "TECHNICAL_PLAN.md"))
	if err != nil {
		t.Fatalf("ScanPlanSections: %v", err)
	}
	if len(defined) < 100 {
		t.Fatalf("ScanPlanSections found only %d sections; the plan has well over 100 — the scan is broken, not the plan", len(defined))
	}

	unresolved, err := CheckSectionRefs(root, []string{"internal", "cmd", "contracts", "docs"}, defined)
	if err != nil {
		t.Fatalf("CheckSectionRefs: %v", err)
	}
	for _, ref := range unresolved {
		t.Errorf("%s cites §%s (%d time(s)), which docs/TECHNICAL_PLAN.md does not define. "+
			"§N and Step N are different namespaces that overlap numerically (see "+
			"internal/ops/sectionref.go) — if you meant an implementation Step, that is a "+
			"scheduling artifact and does not belong in code at all: cite the technical-plan "+
			"section that actually governs the behavior instead, or describe the behavior "+
			"directly with no citation if no section does.",
			ref.File, ref.Section, ref.Count)
	}
}

// TestScanPlanSections_FindsBothHeadingLevelsAndTheNumberedList pins the three
// shapes a citation can legitimately resolve to, so a future refactor of
// ScanPlanSections cannot quietly stop recognising one of them and turn
// TestNoSectionRefDrift into a check that passes by finding nothing.
func TestScanPlanSections_FindsBothHeadingLevelsAndTheNumberedList(t *testing.T) {
	root := repoRoot(t)

	defined, err := ScanPlanSections(filepath.Join(root, "docs", "TECHNICAL_PLAN.md"))
	if err != nil {
		t.Fatalf("ScanPlanSections: %v", err)
	}

	for _, want := range []string{
		"8",    // a top-level "## 8. Feature set" heading
		"8.2",  // an item of §8's numbered list, which no ### heading backs
		"27.1", // an ordinary "### 27.1" subsection
	} {
		if !defined[want] {
			t.Errorf("ScanPlanSections did not find §%s; citations to it would be reported as dangling", want)
		}
	}

	// A section number well past the end of the document must NOT resolve;
	// otherwise the check would accept anything.
	const farPastEnd = "999"

	if defined[farPastEnd] {
		t.Errorf("ScanPlanSections resolved section %s; the scan is matching too broadly to catch a real dangling citation", farPastEnd)
	}
}
