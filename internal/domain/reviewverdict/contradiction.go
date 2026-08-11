package reviewverdict

// Outcome is one auto_approval_outcomes row's own outcome column (§21.2
// stage 2, migrations/000070_auto_approval_outcomes.up.sql) -- a closed,
// two-value vocabulary living here in Go, never a Postgres ENUM, mirroring
// review_findings.status/sentinel_kind's own established "the schema
// stays TEXT, a sibling Go package is the source of truth" precedent
// (that table's own migration doc comment).
type Outcome string

const (
	// OutcomeConfirmed is an auto-approved PR that was actually merged
	// (human 1-click confirm, or the armed auto-merge worker) -- the
	// engine's own judgment stood.
	OutcomeConfirmed Outcome = "confirmed"
	// OutcomeOverridden is a human disagreeing with an auto-approved
	// verdict BEFORE any merge happened (GitHub's own HasChangesRequested
	// became true, or a review:needs-human label was applied).
	OutcomeOverridden Outcome = "overridden"
)

// ContradictionRate computes §21.2's own calibration metric -- "the
// fraction of auto-approved PRs a human later disagreed with" -- as
// contested/total. ok=false when total is zero: no auto-approval outcome
// has been recorded yet in the queried window, a legitimate "not yet
// computed" answer a caller must never render identically to a real,
// computed 0% contradiction rate (§21.1's own sentinel discipline,
// doc.go) -- the whole reason an admin arming the auto-merge toggle for a
// brand-new repo must see "no data yet", never a falsely reassuring "0%
// so far".
//
// contested is expected to be <= total (every OutcomeOverridden row is
// also counted in total, since both share the SAME underlying query --
// internal/app/reviewverdict's own CountAutoApprovalOutcomesInWindow);
// this function does not itself validate that relationship (a pure
// arithmetic reduction over values the caller's own query already
// guarantees are consistent, mirroring MedianLatency's own "caller
// fetches, this package only reduces" split), but the returned rate is
// naturally in [0, 1] whenever the caller's own invariant holds.
func ContradictionRate(total, contested int) (rate float64, ok bool) {
	if total == 0 {
		return 0, false
	}
	return float64(contested) / float64(total), true
}
