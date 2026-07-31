package reviewpost

import (
	"errors"
	"strings"

	"github.com/khazaddev/narvi/internal/domain/review"
)

// VerdictInput is the shape of a review-verdict-posting-tool call's own
// typed fields (§8.2/Step 47) BEFORE the server recomputes the
// authoritative Shippable -- one field per Verdict field EXCEPT Shippable
// itself (never accepted from a caller, per review.Verdict's own CONTRACT)
// plus Summary, the agent's own human-readable narrative body (review's
// own doc.go design call #4: no Finding/narrative shape exists in that
// package yet, so this is carried here, one level up, until a later Step
// gives it a richer, structured home).
type VerdictInput struct {
	RiskLevel         review.RiskLevel
	Premise           review.PremiseState
	BlastRadius       []review.Tag
	FilesChanged      int
	TestsCoverage     review.TestsCoverageState
	DocsDrift         review.DocsDriftState
	ProposedShippable review.ProposedShippable
	// Summary is the agent's own free-text narrative -- required
	// (ValidateVerdictInput rejects an empty/whitespace-only value): a
	// verdict with no human-readable explanation at all defeats the point
	// of posting one. Never parsed back out as structured data by anything
	// in this codebase -- see review/doc.go's own top-level "nothing here
	// even imports a markdown parser, on principle" stance, which this
	// field's own handling matches: it is rendered once (rendercomment.go)
	// and never read again.
	Summary string
}

// The errors ValidateVerdictInput returns -- one per rejected field, named
// distinctly so a caller (internal/adapters/inbound/httpapi/
// reviewverdict.go) can render a specific, actionable 400 body rather than
// a single generic "invalid payload" message, and so a table-driven test
// can assert exactly WHICH check fired for a given malformed/partial
// input.
var (
	ErrInvalidRiskLevel         = errors.New("reviewpost: riskLevel must be one of low/medium/high")
	ErrInvalidPremise           = errors.New("reviewpost: premise must be one of ok/questionable/not_a_pr")
	ErrInvalidTestsCoverage     = errors.New("reviewpost: testsCoverage must be one of adequate/insufficient/skipped")
	ErrInvalidDocsDrift         = errors.New("reviewpost: docsDrift must be one of none/found/skipped")
	ErrInvalidProposedShippable = errors.New("reviewpost: proposedShippable must be one of auto/needs_human/block")
	ErrInvalidBlastRadiusTag    = errors.New("reviewpost: blastRadius contains an unrecognized tag")
	ErrNegativeFilesChanged     = errors.New("reviewpost: filesChanged must not be negative")
	ErrEmptySummary             = errors.New("reviewpost: summary must not be empty")
)

// ValidateVerdictInput rejects a malformed or partial verdict-posting-tool
// call -- every enum field is checked against review's own EXPORTED
// consts (never a bare string comparison against a literal, so a future
// addition to any of those enums only ever needs a switch case added
// here, never a parallel vocabulary invented independently). Checked in a
// fixed order (RiskLevel, Premise, TestsCoverage, DocsDrift,
// ProposedShippable, BlastRadius, FilesChanged, Summary) so a caller
// presenting more than one bad field always gets the SAME, deterministic
// first error rather than one that depends on map iteration order or
// similar.
//
// Every one of review's four "closed enum" types has a Go zero value
// ("") that is deliberately not a legal member (review/doc.go's own
// "fail-conservative policy for every closed enum" section) -- an absent
// JSON field decodes to that same zero value, so a MISSING field and a
// GARBLED one are rejected by the identical check here, never
// distinguished (there is no principled difference between "the caller
// forgot this field" and "the caller sent a value this package doesn't
// recognize" from a validation standpoint -- both mean the payload cannot
// be trusted to construct an authoritative Verdict).
func ValidateVerdictInput(in VerdictInput) error {
	switch in.RiskLevel {
	case review.RiskLevelLow, review.RiskLevelMedium, review.RiskLevelHigh:
	default:
		return ErrInvalidRiskLevel
	}

	switch in.Premise {
	case review.PremiseStateOK, review.PremiseStateQuestionable, review.PremiseStateNotAPR:
	default:
		return ErrInvalidPremise
	}

	switch in.TestsCoverage {
	case review.TestsCoverageStateAdequate, review.TestsCoverageStateInsufficient, review.TestsCoverageStateSkipped:
	default:
		return ErrInvalidTestsCoverage
	}

	switch in.DocsDrift {
	case review.DocsDriftStateNone, review.DocsDriftStateFound, review.DocsDriftStateSkipped:
	default:
		return ErrInvalidDocsDrift
	}

	switch in.ProposedShippable {
	case review.ProposedShippableAuto, review.ProposedShippableNeedsHuman, review.ProposedShippableBlock:
	default:
		return ErrInvalidProposedShippable
	}

	for _, tag := range in.BlastRadius {
		switch tag {
		case review.TagAuth, review.TagMigrations, review.TagContracts, review.TagSecrets,
			review.TagInfra, review.TagPublicAPI, review.TagDataLayer, review.TagDependencies:
		default:
			return ErrInvalidBlastRadiusTag
		}
	}

	if in.FilesChanged < 0 {
		return ErrNegativeFilesChanged
	}

	if strings.TrimSpace(in.Summary) == "" {
		return ErrEmptySummary
	}

	return nil
}

// BuildVerdict is the ONE sanctioned way this package turns an
// ALREADY-VALIDATED VerdictInput (ValidateVerdictInput must be called
// first -- BuildVerdict does not re-validate) into an authoritative
// review.Verdict: Shippable is populated with EXACTLY
// review.ComputeShippable's own return value, never in.ProposedShippable
// converted, matching review.Verdict's own CONTRACT to the letter (the
// caller's ProposedShippable is still carried onto the result, verbatim,
// as pure audit/transparency data -- it simply never influences
// Shippable's own computation, since ComputeShippable's signature does not
// accept it at all).
func BuildVerdict(in VerdictInput) review.Verdict {
	return review.Verdict{
		RiskLevel:         in.RiskLevel,
		Premise:           in.Premise,
		BlastRadius:       in.BlastRadius,
		FilesChanged:      in.FilesChanged,
		TestsCoverage:     in.TestsCoverage,
		DocsDrift:         in.DocsDrift,
		ProposedShippable: in.ProposedShippable,
		Shippable:         review.ComputeShippable(in.RiskLevel, in.TestsCoverage, in.Premise),
	}
}
