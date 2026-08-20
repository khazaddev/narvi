package reviewverdict

import (
	"sort"

	"github.com/khazaddev/narvi/internal/domain/reviewpost"
)

// FindingStatusCount is one reviewpost.FindingStatus's own occurrence
// count across a window's worth of review_findings rows -- FindingOutcomes'
// own per-status output row.
type FindingStatusCount struct {
	Status reviewpost.FindingStatus
	Count  int
}

// FindingOutcomes counts statuses across statuses -- §21.1/§12.2 item 6's
// own "Review finding outcomes" KPI, read from review_findings (Step 48),
// the durable, per-finding MUTABLE-status history review_verdicts
// (append-only, no per-finding status of its own) never carries -- see
// migrations/000046_review_findings.up.sql's own doc comment for why a
// finding's status lives there and not on the verdict. Deliberately the
// RAW reviewpost.FindingStatus distribution (open/rebutted/fix_pending/
// fix_open/fix_merged/fix_applied) rather than a collapsed
// accepted/rebutted/dismissed 3-tier label: that collapsing is a
// PRESENTATION decision (mockups.html's own "Review finding outcomes"
// tile), left to whichever future work actually builds that UI (§14.4, out
// of this file's own scope) rather than guessed at here.
//
// Sorted by Count descending, tie-broken alphabetically by Status, the
// SAME deterministic-ordering discipline TopRiskDrivers already
// establishes for an analogous map-shaped rollup.
//
// ok=false for an empty statuses slice, mirroring Timeseries/
// TopRiskDrivers' own identical "not yet computed" sentinel.
func FindingOutcomes(statuses []reviewpost.FindingStatus) ([]FindingStatusCount, bool) {
	if len(statuses) == 0 {
		return nil, false
	}

	counts := make(map[reviewpost.FindingStatus]int)
	for _, s := range statuses {
		counts[s]++
	}

	result := make([]FindingStatusCount, 0, len(counts))
	for status, count := range counts {
		result = append(result, FindingStatusCount{Status: status, Count: count})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Count != result[j].Count {
			return result[i].Count > result[j].Count
		}
		return result[i].Status < result[j].Status
	})
	return result, true
}
