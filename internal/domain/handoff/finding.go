package handoff

import (
	"fmt"

	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/domain/reviewpost"
)

// Label is the fixed GitHub label v1's own "auto-apply a handoff label"
// requirement (§14.4) syncs onto a scoped-session PR that has anything to
// report -- a single, fixed string, never derived from a verdict's own
// RiskLevel the way reviewpost.RiskLabel is: this sentinel never computes
// a risk verdict at all (§14.4: "alongside or INSTEAD OF a normal risk
// verdict").
const Label = "handoff"

// contractsGlob is ContractDriftFinding's own symbolic FilePath -- not a
// real, single file (contract drift is a repo-level signal, doc.go's own
// design call #2), but the same glob §14.4's own text names
// ("contracts/api/*"), so a reader immediately recognizes what area this
// finding is about.
const contractsGlob = "contracts/api/*"

// ContractDriftFinding builds the FindingInput for repoFullName's own
// repo-level contract-drift signal (doc.go's design call #2) --
// reviewpost.FindingInput/BuildFinding/ComputeFindingIdentity are reused
// verbatim (never a parallel Finding shape or identity scheme); SentinelKind
// is always nil (doc.go's design call #1: this finding must never become
// eligible for the UNRELATED sentinel-auto-fix flow).
func ContractDriftFinding(repoFullName string) reviewpost.FindingInput {
	return reviewpost.FindingInput{
		Severity: review.RiskLevelMedium,
		FilePath: contractsGlob,
		Description: fmt.Sprintf(
			"%s's own backend appears to have changed since its declared contract (%s) was last checked -- some endpoint(s) this prototype calls may have no matching contract entry. Verify %s is current before handing this off.",
			repoFullName, contractsGlob, contractsGlob,
		),
	}
}

// TODOFindingInput builds the FindingInput for one TODOFinding ScanTODOs
// found -- SentinelKind is always nil, exactly like ContractDriftFinding
// above and for the identical reason.
func TODOFindingInput(f TODOFinding) reviewpost.FindingInput {
	line := f.Line
	return reviewpost.FindingInput{
		Severity:    review.RiskLevelLow,
		FilePath:    f.FilePath,
		Line:        &line,
		Description: fmt.Sprintf("Backend-adjacent TODO left in a scoped session's own diff: %s", f.Text),
	}
}

// BuildFindingInputs assembles every FindingInput this sentinel run has
// to report, in a fixed, deterministic order (contract-drift first, then
// TODOs in ScanTODOs' own encounter order) -- a pure function: the caller
// (sessionactor's own handoffsentinel.go) is responsible for the impure
// half (resolving contractDrifted and diff), this function only ever
// shapes already-resolved inputs into typed FindingInput values.
func BuildFindingInputs(repoFullName string, contractDrifted bool, todos []TODOFinding) []reviewpost.FindingInput {
	var inputs []reviewpost.FindingInput
	if contractDrifted {
		inputs = append(inputs, ContractDriftFinding(repoFullName))
	}
	for _, t := range todos {
		inputs = append(inputs, TODOFindingInput(t))
	}
	return inputs
}
