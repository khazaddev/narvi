package review

// PremiseState is the reviewer's own assessment of whether a PR's stated
// premise (why this change, what problem it solves) holds up — exactly the
// three states §8.2/Step 45 names: ok, questionable, not_a_pr.
type PremiseState string

// The three PremiseState values. The zero value ("") is deliberately not
// one of them — see PremiseFloor for how an unrecognized value is
// handled.
const (
	// PremiseStateOK is the PR's stated premise holding up under review.
	PremiseStateOK PremiseState = "ok"
	// PremiseStateQuestionable is something about the stated premise not
	// fully adding up, short of the reviewer concluding the PR itself is
	// illegitimate.
	PremiseStateQuestionable PremiseState = "questionable"
	// PremiseStateNotAPR is the reviewer concluding the diff is not a
	// legitimate code change to review at all (e.g. an empty/no-op diff,
	// content that does not correspond to a real PR, spam). This
	// package's own fail-conservative reference point for an unrecognized
	// PremiseState value (see PremiseFloor).
	PremiseStateNotAPR PremiseState = "not_a_pr"
)

// PremiseFloor is the premise raise-only floor's single exported pure
// function (§8.2/Step 45): given the reviewer's own PremiseState, it
// returns the MOST CONSERVATIVE Shippable value premise alone ever forces.
// This function alone decides nothing about whether a PR ships — see
// ComputeShippable (shippable.go) for the actual composition; a floor may
// only ever be a lower bound a wider composition raises UP TO, never a
// value that composition lowers.
//
//   - PremiseStateOK: ShippableAuto — premise imposes no floor of its own.
//   - PremiseStateQuestionable: ShippableNeedsHuman — a person should look
//     before this goes anywhere near auto-approval, though the reviewer
//     stopped short of concluding the PR is illegitimate.
//   - PremiseStateNotAPR: ShippableBlock — "needs a human to review it as
//     code" undersells this case; there is no code review to conduct
//     until a human sorts out what this even is, so it is treated as a
//     hard floor breach, not merely "needs human eyes".
//   - the zero value or any other unrecognized PremiseState: ShippableBlock,
//     FAILING CONSERVATIVE — an unrecognized premise assessment carries
//     even less confidence than a confirmed PremiseStateNotAPR
//     determination (doc.go's uniform fail-conservative policy), so it is
//     never ranked any lower.
func PremiseFloor(s PremiseState) Shippable {
	switch s {
	case PremiseStateOK:
		return ShippableAuto
	case PremiseStateQuestionable:
		return ShippableNeedsHuman
	case PremiseStateNotAPR:
		return ShippableBlock
	default:
		return ShippableBlock
	}
}
