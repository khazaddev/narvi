package reviewpost

// FactCheckStatus is the diff-only fact-check pass's own outcome (§26.6,
// Step 69, "diff-only fact-check pass, both paths"): whether the primary
// reviewer's orchestration spawned the fact-check sub-task (§7.1's
// engine-native fan-out, configured with NO tool access) before posting
// this verdict.
//
// Deliberately placed in THIS package, never in internal/domain/review
// alongside review.CounterReviewStatus (counterreview.go): unlike
// CounterReviewStatus, this type feeds NO Shippable floor at all -- it
// has no business in review's own closed, floor-input-only universe (that
// package's own doc.go: "exactly eight exported functions... no second
// path to any of these results"). See this type's own doc comment below
// for the full "why never a floor" reasoning -- the SAME reasoning that
// makes it safe for this pass to run on the light path in the first
// place (§26.9's own "removes findings... costs nothing rigor bears on"
// argument).
type FactCheckStatus string

// The two legal FactCheckStatus values (§26.6's own "typed FactCheck:
// done|skipped field", verbatim) -- unlike review.CounterReviewStatus,
// this type is schema-required UNCONDITIONALLY (both paths, ValidateVerdictInput's
// own ErrInvalidFactCheck check, never gated behind ReviewDepth), so its
// own zero value ("") is rejected on EVERY verdict, not merely a deep-path
// one.
const (
	// FactCheckDone is the primary reviewer's orchestration having
	// actually spawned the fact-check sub-task and applied whatever
	// prunes it returned (possibly zero, FactCheckKilled == 0) before
	// posting this verdict.
	FactCheckDone FactCheckStatus = "done"
	// FactCheckSkipped is a verdict whose fact-check sub-task did NOT
	// run -- for any cause: a provider failure inside the sub-task, a
	// timeout, malformed output (§26.6's own "non-fatal on error... fails
	// open, same posture as triage"), or (§26.7) the per-path cost
	// ceiling already having been reached before this optional pass would
	// have been dispatched.
	//
	// # Why this NEVER raises Shippable -- the deliberate, load-bearing
	// difference from review.CounterReviewSkipped
	//
	// A skipped fact-check pass means exactly one thing: the findings
	// this verdict publishes were not additionally pruned of any
	// provably-wrong ones. It can NEVER mean a real defect went
	// unverified -- the pass, by construction (§26.6: "tries only to
	// DISPROVE each finding... kills a finding only when it is provably
	// wrong from the diff alone"), can only REMOVE findings, never add or
	// vouch for one. Its absence can therefore only make the published
	// appendix noisier (more findings, some of which might have been
	// prunable), never LESS SAFE -- there is no equivalent safety claim
	// for a skipped fact-check to under-deliver on the way a skipped
	// counter-review under-delivers on "this sensitive/sizable PR got
	// adversarial scrutiny". See internal/domain/review.CounterReviewFloor
	// (counterreview.go) for the floor this type deliberately has no
	// analogue of, and reviewpost.BuildVerdict's own doc comment for
	// where CounterReviewStatus (not this type) actually reaches
	// ComputeShippable.
	FactCheckSkipped FactCheckStatus = "skipped"
)
