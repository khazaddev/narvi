package review

// DocsDriftState is the doc-drift sentinel's own assessed state for a PR
// (§8.2) — whether documentation appears to have fallen out of sync with
// the change. Carried on Verdict as data; NOT wired into ComputeShippable
// in this Step (only the coverage and premise floors exist per §8.2/Step
// 45 — see doc.go's design call #2 and #5). A future doc-drift floor, if
// one is ever added, should treat an unrecognized DocsDriftState the same
// way this package treats every other enum's unrecognized value: exactly
// as conservatively as the worst known legitimate state
// (DocsDriftStateFound), never more leniently. That policy is recorded
// here, on the type, so it is not invented ad hoc by whichever future Step
// adds the floor.
type DocsDriftState string

// The three DocsDriftState values. The zero value ("") is deliberately not
// one of them.
const (
	// DocsDriftStateNone is the drift sentinel finding no discrepancy.
	DocsDriftStateNone DocsDriftState = "none"
	// DocsDriftStateFound is the drift sentinel finding documentation
	// that appears stale relative to the diff. This package's own
	// fail-conservative reference point for an unrecognized
	// DocsDriftState value (see the type's own doc comment above).
	DocsDriftStateFound DocsDriftState = "found"
	// DocsDriftStateSkipped is a DELIBERATE, already-made decision that
	// doc-drift assessment does not apply to this change (e.g. no
	// documentation exists for the touched area at all) — categorically
	// different from "nobody ever checked" (the zero value), the same
	// distinction TestsCoverageStateSkipped draws from an unrecognized
	// coverage state (coverage.go).
	DocsDriftStateSkipped DocsDriftState = "skipped"
)
