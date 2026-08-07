package decisioninbox

import (
	"sort"
	"time"
)

// MedianLatency computes the median of durations -- §16.2's own "decision
// latency (median time from item entering the queue to its action)"
// metric. ok=false for an empty input (no yet-actioned items in the
// window this Step's own app-layer caller bounds its query to -- a
// legitimate "not yet computed" outcome, never a zero duration standing in
// for it; the app layer surfaces that distinction onward exactly like
// §21.1's own "not yet computed sentinel, distinct from a real zero"
// requirement for every OTHER analytics rollup in this codebase).
//
// durations is never mutated by this function (a local copy is sorted).
// For an even-length input, the median is the average of the two middle
// values -- the standard definition, and the one that avoids an arbitrary
// "pick the lower/upper middle" tie-break the caller would otherwise have
// to justify.
func MedianLatency(durations []time.Duration) (time.Duration, bool) {
	if len(durations) == 0 {
		return 0, false
	}

	sorted := make([]time.Duration, len(durations))
	copy(sorted, durations)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid], true
	}
	return (sorted[mid-1] + sorted[mid]) / 2, true
}
