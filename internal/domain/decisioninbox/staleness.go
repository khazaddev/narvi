package decisioninbox

import "time"

// IsStale reports whether a row that entered the queue at enteredQueueAt
// is stale as of now, given threshold -- §16.1: "stale items (>48h,
// configurable) visually flagged". The default threshold lives in
// platform.Timeouts.DecisionInboxStaleAfter (this package never reads
// platform directly -- CLAUDE.md/§11: no I/O, and platform.Timeouts is
// itself sourced from process config, which this pure package must stay
// ignorant of); the caller supplies it.
//
// A zero enteredQueueAt (the caller could not determine one -- should not
// happen for any row this package's own app-layer caller actually builds,
// but defended against anyway) is never reported stale: an unknown age is
// not evidence of staleness, mirroring this codebase's own established
// "never assert a fact without positive confirmation" discipline (e.g.
// ports.RevertReviewStateUnknown's own doc comment).
func IsStale(enteredQueueAt, now time.Time, threshold time.Duration) bool {
	if enteredQueueAt.IsZero() {
		return false
	}
	return now.Sub(enteredQueueAt) > threshold
}

// Age returns how long a row has been in the queue, as of now -- zero for
// a zero enteredQueueAt (mirrors IsStale's own "unknown means unknown, not
// evidence of anything" discipline immediately above).
func Age(enteredQueueAt, now time.Time) time.Duration {
	if enteredQueueAt.IsZero() {
		return 0
	}
	return now.Sub(enteredQueueAt)
}
