package review

// TestsCoverageState is the test-coverage sentinel's own assessed state for
// a PR (§8.2).
type TestsCoverageState string

// The three TestsCoverageState values. The zero value ("") is deliberately
// not one of them — see CoverageFloor for how an unrecognized value is
// handled.
const (
	// TestsCoverageStateAdequate is the coverage sentinel finding every
	// new/changed code path covered.
	TestsCoverageStateAdequate TestsCoverageState = "adequate"
	// TestsCoverageStateInsufficient is the coverage sentinel finding a
	// gap. This package's own fail-conservative reference point for an
	// unrecognized TestsCoverageState value (see CoverageFloor).
	TestsCoverageStateInsufficient TestsCoverageState = "insufficient"
	// TestsCoverageStateSkipped is a DELIBERATE, already-made decision
	// that coverage assessment does not apply to this change (e.g. a
	// docs-only or config-only diff with no coverable code paths at all)
	// — categorically different from "nobody ever checked" (the zero
	// value): a considered "not applicable", not an absence of
	// information.
	TestsCoverageStateSkipped TestsCoverageState = "skipped"
)

// CoverageFloor is the coverage raise-only floor's single exported pure
// function (§8.2): given the test-coverage sentinel's own assessed
// state, it returns the MOST CONSERVATIVE Shippable value coverage alone
// ever forces. This function alone decides nothing about whether a PR
// ships — see ComputeShippable (shippable.go) for the actual composition;
// a floor may only ever be a lower bound a wider composition raises UP TO,
// never a value that composition lowers.
//
//   - TestsCoverageStateAdequate: ShippableAuto — coverage imposes no
//     floor of its own.
//   - TestsCoverageStateSkipped: ShippableAuto — a deliberate decision
//     that coverage does not apply here, not an absence of information;
//     treated identically to Adequate for this reason.
//   - TestsCoverageStateInsufficient: ShippableNeedsHuman — a real signal,
//     but a coverage gap alone does not by itself mean the change must be
//     blocked outright; a human reviewer is the right gate.
//   - the zero value or any other unrecognized TestsCoverageState:
//     ShippableNeedsHuman, FAILING CONSERVATIVE — an unassessed coverage
//     state carries strictly LESS information than a confirmed
//     Insufficient (we don't even know if there is a code-path gap), but
//     by doc.go's uniform fail-conservative policy it must never rank
//     BELOW Insufficient's own floor, so it is deliberately given the
//     identical one, not a weaker one.
func CoverageFloor(s TestsCoverageState) Shippable {
	switch s {
	case TestsCoverageStateAdequate, TestsCoverageStateSkipped:
		return ShippableAuto
	case TestsCoverageStateInsufficient:
		return ShippableNeedsHuman
	default:
		return ShippableNeedsHuman
	}
}
