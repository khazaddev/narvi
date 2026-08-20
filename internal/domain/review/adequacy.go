package review

// DescriptionAdequacy is the reviewer's own tri-state assessment (§26.2,
// "review digest: description adequacy + graduated remediation")
// of whether a PR's title+body honestly represent what its diff actually
// does -- the reviewing agent's own comparison of its diff-derived
// Digest.Summary (internal/domain/reviewpost, §26.1's own §26.1
// addition) against the PR's title+body, which stay untrusted INPUT
// throughout (§5.2): the comparison consumes them, never obeys them. The
// zero value ("") is deliberately not one of the three named values --
// see AdequacyFloor below for how an unrecognized value is handled, the
// SAME uniform fail-conservative policy PremiseState/TestsCoverageState
// already establish (review/doc.go).
type DescriptionAdequacy string

// The three DescriptionAdequacy values (§26.2's own "ok|drift|misleading"
// tri-state, verbatim).
const (
	// DescriptionAdequacyOK is the PR's own title+body accurately
	// representing what the diff does -- no floor of its own.
	DescriptionAdequacyOK DescriptionAdequacy = "ok"
	// DescriptionAdequacyDrift is the PR's own title+body having fallen
	// out of sync with the diff (stale, incomplete, or missing a since-
	// added concern) SHORT of actively misrepresenting it -- §26.2 only
	// ever floors on the STRONGER "misleading" value below, so drift
	// alone imposes no floor here either (see AdequacyFloor).
	DescriptionAdequacyDrift DescriptionAdequacy = "drift"
	// DescriptionAdequacyMisleading is the PR's own title+body actively
	// misrepresenting what the diff does -- §26.2's own named floor
	// trigger. This package's own fail-conservative reference point for
	// an unrecognized DescriptionAdequacy value (see AdequacyFloor).
	DescriptionAdequacyMisleading DescriptionAdequacy = "misleading"
)

// AdequacyFloor is the THIRD raise-only floor's single exported pure
// function (§26.2, composing alongside CoverageFloor/PremiseFloor via the
// existing max(rank) in ComputeShippable, §8.2's own
// exactly-one-pure-function-per-floor pattern extended by this Step):
// given the reviewer's own DescriptionAdequacy, it returns the MOST
// CONSERVATIVE Shippable value adequacy alone ever forces. This function
// alone decides nothing about whether a PR ships -- see ComputeShippable
// (shippable.go) for the actual composition; a floor may only ever be a
// lower bound a wider composition raises UP TO, never a value that
// composition lowers.
//
// Deliberate asymmetry, stated explicitly by §26.2 itself: this floor
// only ever raises Shippable, and NEVER touches RiskLevel -- the server
// computes Shippable, but never fabricates risk the model did not report
// (review.Verdict's own RiskLevel field is set from the model's own
// self-reported assessment, upstream of and untouched by this function).
//
//   - DescriptionAdequacyOK: ShippableAuto -- an honest description
//     imposes no floor of its own.
//   - DescriptionAdequacyDrift: ShippableAuto -- §26.2 names ONLY
//     "misleading" as this Step's own floor trigger; a merely stale or
//     incomplete description, short of actively misrepresenting the
//     diff, does not by itself require a human gate.
//   - DescriptionAdequacyMisleading: ShippableNeedsHuman -- §26.2's own
//     named floor: "misleading floors Shippable at needs_human".
//   - the zero value or any other unrecognized DescriptionAdequacy:
//     ShippableNeedsHuman, FAILING CONSERVATIVE -- ranked identically to
//     DescriptionAdequacyMisleading, this enum's own worst known
//     legitimate value (doc.go's uniform fail-conservative policy), never
//     as permissive as DescriptionAdequacyOK/Drift's own ShippableAuto.
func AdequacyFloor(a DescriptionAdequacy) Shippable {
	switch a {
	case DescriptionAdequacyOK, DescriptionAdequacyDrift:
		return ShippableAuto
	case DescriptionAdequacyMisleading:
		return ShippableNeedsHuman
	default:
		return ShippableNeedsHuman
	}
}
