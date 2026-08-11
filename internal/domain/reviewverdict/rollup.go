package reviewverdict

import (
	"sort"
	"time"

	"github.com/khazaddev/narvi/internal/domain/review"
)

// DayBucket is one calendar day's own Shippable-classification counts --
// Timeseries' own per-day output row.
type DayBucket struct {
	// Day is the UTC calendar day this bucket covers, truncated to
	// midnight -- never a caller-supplied "now", always derived from a
	// Record's own already-fetched CreatedAt.
	Day time.Time
	// Counts maps each review.Shippable value present that day to how
	// many verdicts posted that day carried it. A Shippable value with
	// zero occurrences that day is simply absent from the map, never a
	// zero entry -- callers that need every one of the three values
	// present iterate review.Shippable's own three known consts directly.
	Counts map[review.Shippable]int
}

// Timeseries buckets records by the UTC calendar day their own CreatedAt
// falls on, counting each day's own Shippable classifications --
// §21.1/§12.2 item 6's own "sessions-per-day stacked by outcome" chart
// shape, applied here to verdicts instead of sessions. records is assumed
// already bounded to the caller's own query window (internal/app/
// reviewverdict's own ListReviewVerdictsInWindow) -- this function adds
// no window of its own.
//
// ok=false for an empty records slice -- §21.1's own "not yet computed"
// sentinel, distinct from a real, computed empty timeseries (doc.go).
func Timeseries(records []Record) ([]DayBucket, bool) {
	if len(records) == 0 {
		return nil, false
	}

	byDay := make(map[time.Time]map[review.Shippable]int)
	var order []time.Time
	for _, r := range records {
		day := r.CreatedAt.UTC().Truncate(24 * time.Hour)
		counts, ok := byDay[day]
		if !ok {
			counts = make(map[review.Shippable]int)
			byDay[day] = counts
			order = append(order, day)
		}
		counts[r.Verdict.Shippable]++
	}

	sort.Slice(order, func(i, j int) bool { return order[i].Before(order[j]) })

	buckets := make([]DayBucket, len(order))
	for i, day := range order {
		buckets[i] = DayBucket{Day: day, Counts: byDay[day]}
	}
	return buckets, true
}

// TagCount is one review.Tag's own occurrence count across a window's
// worth of verdicts -- TopRiskDrivers' own per-tag output row.
type TagCount struct {
	Tag   review.Tag
	Count int
}

// TopRiskDrivers counts how many records' own BlastRadius includes each
// review.Tag, across the whole window -- §21.1/§12.2 item 6's own
// "top-risk-driver breakdown". A verdict whose BlastRadius names the same
// tag is only possible once per verdict (BlastRadius is a set in
// practice, per review.Verdict's own doc comment), so this is a straight
// per-verdict, per-tag increment, never double-counted within one
// verdict even if a caller somehow constructed a Record with a
// duplicate-tag BlastRadius.
//
// Sorted by Count descending, tie-broken alphabetically by Tag (a fixed,
// deterministic order -- never "whichever tag happened to be inserted
// into an internal map first", which map iteration order in Go would
// otherwise make nondeterministic across calls).
//
// ok=false for an empty records slice, mirroring Timeseries' own
// identical "not yet computed" sentinel.
func TopRiskDrivers(records []Record) ([]TagCount, bool) {
	if len(records) == 0 {
		return nil, false
	}

	counts := make(map[review.Tag]int)
	for _, r := range records {
		for _, tag := range r.Verdict.BlastRadius {
			counts[tag]++
		}
	}
	if len(counts) == 0 {
		// Every record in the window had an empty BlastRadius -- a real,
		// computed answer ("no risk drivers were tagged"), not "no
		// data" (records itself was non-empty) -- so this returns
		// ok=true with a nil/empty slice, never the ok=false sentinel
		// above, which means something categorically different (no
		// verdicts existed at all).
		return nil, true
	}

	result := make([]TagCount, 0, len(counts))
	for tag, count := range counts {
		result = append(result, TagCount{Tag: tag, Count: count})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Count != result[j].Count {
			return result[i].Count > result[j].Count
		}
		return result[i].Tag < result[j].Tag
	})
	return result, true
}
