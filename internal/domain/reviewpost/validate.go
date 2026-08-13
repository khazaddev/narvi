package reviewpost

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/domain/reviewtriage"
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

	// Findings is Step 48's own additive extension (§8.2/§17/§22.1): zero
	// or more per-finding typed fields, alongside the verdict's own
	// aggregate fields above -- restdtos.PostReviewVerdictRequest.findings
	// is OPTIONAL, so an old caller posting no findings at all (every
	// caller before this Step) keeps posting exactly as before: nil/empty
	// is a fully legitimate value, never rejected by ValidateVerdictInput
	// below. See finding.go's own doc comment for why this type lives
	// here, in reviewpost, rather than as a new review.Verdict field.
	Findings []FindingInput

	// Digest is Step 66's own additive extension (§26.1): the merge-
	// readout's typed content -- restdtos.PostReviewVerdictRequest.digest
	// is REQUIRED (unlike Findings above), and Digest.Summary within it is
	// the one field ValidateVerdictInput below actually enforces this
	// Step; ArchDecisions/StackRisks/UnverifiedLimits are requested (the
	// review-turn prompt, review/context.go, asks the agent to fill them)
	// but not yet validation-enforced -- see digest.go's own doc comment
	// for the full "why", and this field's own struct type for why it
	// lives here rather than as a new review.Verdict field.
	Digest Digest

	// ReviewDepth (Step 68, §26.3) is the posting turn's own resolved
	// light/deep routing decision (turns.review_depth) -- threaded
	// through by the caller (internal/adapters/inbound/httpapi/
	// reviewverdict.go), never accepted from the agent's own POST body
	// (mirrors Shippable's own "server-computed only" contract: the
	// AGENT never gets to claim which path it ran on). The zero value
	// (reviewtriage.ReviewDepth("")) means "no resolvable depth for this
	// turn" -- ValidateVerdictInput below treats that identically to
	// DepthLight (never requiring the full digest), since a turn with no
	// recorded depth was never routed deep by construction.
	ReviewDepth reviewtriage.ReviewDepth
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
	// ErrEmptyDigestSummary is Step 66's own addition (§26.1): "Digest.Summary
	// is required on every review from Step 66 on" -- mirrors ErrEmptySummary
	// above exactly (same empty/whitespace-only check, same "missing and
	// garbled are the identical failure" posture), for the digest's own
	// "what this PR does" field rather than the verdict's overall narrative.
	ErrEmptyDigestSummary = errors.New("reviewpost: digest.summary must not be empty")
	// ErrInvalidDescriptionAdequacy is §26.2/Step 67's own addition:
	// digest.descriptionAdequacy must be one of review's own three closed
	// DescriptionAdequacy values -- mirrors ErrInvalidPremise/
	// ErrInvalidRiskLevel's own identical closed-enum check shape, applied
	// here rather than treated like Digest.Summary's weaker "just
	// non-blank" check, since DescriptionAdequacy is a validated enum that
	// directly feeds review.ComputeShippable's own third floor, not free
	// text.
	ErrInvalidDescriptionAdequacy = errors.New("reviewpost: digest.descriptionAdequacy must be one of ok/drift/misleading")
	// ErrEmptyAdequacyExplanation is §26.2/Step 67's own addition:
	// digest.adequacyExplanation must not be empty/whitespace-only --
	// mirrors ErrEmptySummary/ErrEmptyDigestSummary's own identical
	// "missing and garbled are the identical failure" posture, for the
	// tri-state's own required one-line explanation.
	ErrEmptyAdequacyExplanation = errors.New("reviewpost: digest.adequacyExplanation must not be empty")
	// ErrEmptyDigestArchDecisions/ErrEmptyDigestStackRisks/
	// ErrEmptyDigestUnverifiedLimits are Step 68's own addition (§26.3,
	// via §26.1's own forward reference: "the full digest ... becomes
	// schema-required on the deep path once §26.3 defines it") -- the
	// three digest fields §26.1/Step 66 requested but deliberately left
	// unenforced ("explicit future work, §26.3/Step 68, once a 'deep
	// path' exists for it to attach to", reviewpost/digest.go's own doc
	// comment) are now REQUIRED, but ONLY when in.ReviewDepth ==
	// reviewtriage.DepthDeep -- see ValidateVerdictInput's own check
	// below. Each mirrors ErrEmptyDigestSummary's identical "missing and
	// garbled are the identical failure" empty/whitespace-only check.
	ErrEmptyDigestArchDecisions    = errors.New("reviewpost: digest.archDecisions must not be empty on a deep-path review")
	ErrEmptyDigestStackRisks       = errors.New("reviewpost: digest.stackRisks must not be empty on a deep-path review")
	ErrEmptyDigestUnverifiedLimits = errors.New("reviewpost: digest.unverifiedLimits must not be empty on a deep-path review")
)

// ValidateVerdictInput rejects a malformed or partial verdict-posting-tool
// call -- every enum field is checked against review's own EXPORTED
// consts (never a bare string comparison against a literal, so a future
// addition to any of those enums only ever needs a switch case added
// here, never a parallel vocabulary invented independently). Checked in a
// fixed order (RiskLevel, Premise, TestsCoverage, DocsDrift,
// ProposedShippable, BlastRadius, FilesChanged, Summary, Digest.Summary,
// Digest.DescriptionAdequacy, Digest.AdequacyExplanation) so a caller
// presenting more than one bad field always gets the SAME, deterministic
// first error rather than one that depends on map iteration order or
// similar. Digest.Summary is checked next-to-last (Step 66, §26.1's own
// new required field), and Digest.DescriptionAdequacy/
// Digest.AdequacyExplanation are checked LAST (§26.2/Step 67's own new
// required fields) -- each added at the end of the existing fixed order
// rather than interleaved earlier, so neither Step ever changes which
// error an EXISTING malformed payload (one that already fails an
// earlier-checked field) was already reporting before it shipped.
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

	// Digest.Summary (Step 66, §26.1): "required on every review from
	// Step 66 on" -- the ONE digest field this Step hard-requires.
	// ArchDecisions/StackRisks/UnverifiedLimits are deliberately NOT
	// checked here at all -- requested via the prompt (review/context.go),
	// not validation-enforced, until §26.3 (a later Step) defines the deep
	// path this package does not implement yet (digest.go's own doc
	// comment).
	if strings.TrimSpace(in.Digest.Summary) == "" {
		return ErrEmptyDigestSummary
	}

	// Digest.DescriptionAdequacy (§26.2/Step 67): a closed-enum check,
	// mirroring RiskLevel/Premise/TestsCoverage/DocsDrift/ProposedShippable
	// above rather than Digest.Summary's own weaker "just non-blank"
	// check -- this field directly feeds review.ComputeShippable's own
	// third raise-only floor (review.AdequacyFloor), so an unvalidated
	// value here would let a garbled/missing assessment silently reach
	// that computation instead of being rejected up front. REQUIRED on
	// every review from this Step on -- see digest.go's own doc comment
	// for why this is NOT deferred behind a future light/deep distinction
	// the way ArchDecisions/StackRisks/UnverifiedLimits still are.
	switch in.Digest.DescriptionAdequacy {
	case review.DescriptionAdequacyOK, review.DescriptionAdequacyDrift, review.DescriptionAdequacyMisleading:
	default:
		return ErrInvalidDescriptionAdequacy
	}

	// Digest.AdequacyExplanation (§26.2/Step 67): "plus a one-line
	// explanation" -- REQUIRED non-blank, mirroring Summary/Digest.
	// Summary's own identical "missing and garbled are the identical
	// failure" treatment for a free-text narrative field.
	if strings.TrimSpace(in.Digest.AdequacyExplanation) == "" {
		return ErrEmptyAdequacyExplanation
	}

	// Digest.ArchDecisions/StackRisks/UnverifiedLimits (Step 68, §26.3,
	// via §26.1's own forward reference): schema-required ONLY on the
	// deep path -- checked LAST, after every field the light path already
	// requires, so a light-path or pre-Step-68 (ReviewDepth == "") caller
	// keeps failing on exactly the SAME first error it always did (this
	// function's own "fixed order" discipline, top doc comment). The
	// posting endpoint's own reject-don't-repair posture (§26.1) means
	// the agent re-submits with a real digest rather than this package
	// ever repairing/fabricating one.
	if in.ReviewDepth == reviewtriage.DepthDeep {
		if len(in.Digest.ArchDecisions) == 0 {
			return ErrEmptyDigestArchDecisions
		}
		if strings.TrimSpace(in.Digest.StackRisks) == "" {
			return ErrEmptyDigestStackRisks
		}
		if strings.TrimSpace(in.Digest.UnverifiedLimits) == "" {
			return ErrEmptyDigestUnverifiedLimits
		}
	}

	// Findings (Step 48, additive): each one validated by
	// ValidateFindingInput, in order -- first bad finding wins, mirroring
	// this function's own fixed-order, first-error-wins discipline above.
	// nil/empty Findings is not iterated at all, so an old caller posting
	// none is never rejected here.
	for _, f := range in.Findings {
		if err := ValidateFindingInput(f); err != nil {
			return err
		}
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
//
// in.Digest.DescriptionAdequacy (§26.2/Step 67) is threaded through as
// ComputeShippable's own fourth argument, the THIRD raise-only floor --
// composed via the SAME max(rank) as coverage/premise, never a special
// case. RiskLevel below is set from in.RiskLevel VERBATIM, completely
// independent of this call: §26.2's own explicit "deliberately never
// inflating RiskLevel" asymmetry is structurally guaranteed here by
// construction, not by any runtime check -- RiskLevel is assigned once,
// above, from the caller's own self-reported field, and nothing about
// in.Digest.DescriptionAdequacy (or any other floor input) can reach that
// assignment.
func BuildVerdict(in VerdictInput) review.Verdict {
	return review.Verdict{
		RiskLevel:         in.RiskLevel,
		Premise:           in.Premise,
		BlastRadius:       in.BlastRadius,
		FilesChanged:      in.FilesChanged,
		TestsCoverage:     in.TestsCoverage,
		DocsDrift:         in.DocsDrift,
		ProposedShippable: in.ProposedShippable,
		Shippable:         review.ComputeShippable(in.RiskLevel, in.TestsCoverage, in.Premise, in.Digest.DescriptionAdequacy),
	}
}

// BuildFindings is BuildVerdict's own per-finding sibling (Step 48,
// additive): turns an ALREADY-VALIDATED VerdictInput.Findings (every
// element already passed ValidateFindingInput, via ValidateVerdictInput's
// own loop above) into the []Finding a caller upserts into review_findings
// -- IdentityHash computed via BuildFinding for each, never client-supplied.
// nil/empty in.Findings returns nil, never a zero-length-but-non-nil
// slice, so a caller's own "did this verdict report any findings at all"
// check can use a plain len()/nil check either way.
//
// Confirmed-finding fix: ComputeFindingIdentity deliberately excludes Line
// (its own doc comment, finding.go) so a finding re-reported at a shifted
// line number still matches its own prior row -- but that SAME property
// means two genuinely DIFFERENT findings in the same file, at different
// lines, that happen to share identical (post-normalization) kind/path/
// description text would otherwise collide onto the identical
// IdentityHash and silently collapse onto the SAME review_findings row
// (UpsertReviewFinding's own ON CONFLICT clause only refreshes
// last_seen_at on a re-post of an already-known identity) -- losing one of
// the two findings entirely, and later making a maintainer's rebuttal of
// "the" finding silently suppress the OTHER, never-looked-at one too, on
// every future re-review (reconcile.go's RenderAlreadyAnsweredFacts).
//
// This function is the one place that can see every finding in the SAME
// verdict submission at once, so it is the one place that can catch this:
// when two or more elements of in.Findings hash to the SAME base identity
// (ComputeFindingIdentity's own kind/path/description triple), each one
// beyond the first is disambiguated by additionally hashing in ITS OWN
// Line (or, absent that, its own position within the colliding group) --
// see disambiguateFindingIdentity below. A base identity that occurs
// exactly once in this batch (the overwhelming common case) is left
// completely untouched, so the ordinary single-finding-per-identity,
// shifted-line-still-matches behavior this package's own tests already
// pin (TestComputeFindingIdentity_SurvivesLineShift) is unaffected.
//
// This deliberately fails toward "an under-match" (two findings that
// really are the SAME issue, reported with a shifted line NUMBER in a
// batch that also happens to contain another same-worded finding, might
// occasionally be treated as distinct across passes) rather than "an
// over-match" (two DIFFERENT findings silently collapsing into one) --
// the identical fail-conservative direction ComputeFindingIdentity's own
// doc comment already commits to for its own residual ambiguity.
func BuildFindings(in VerdictInput) []Finding {
	if len(in.Findings) == 0 {
		return nil
	}

	baseIdentities := make([]string, len(in.Findings))
	counts := make(map[string]int, len(in.Findings))
	for i, f := range in.Findings {
		h := ComputeFindingIdentity(f.SentinelKind, f.FilePath, f.Description)
		baseIdentities[i] = h
		counts[h]++
	}

	seenInGroup := make(map[string]int, len(in.Findings))
	out := make([]Finding, len(in.Findings))
	for i, f := range in.Findings {
		base := baseIdentities[i]
		identity := base
		if counts[base] > 1 {
			identity = disambiguateFindingIdentity(base, f.Line, seenInGroup[base])
			seenInGroup[base]++
		}
		out[i] = Finding{
			IdentityHash: identity,
			SentinelKind: f.SentinelKind,
			Severity:     f.Severity,
			FilePath:     f.FilePath,
			Line:         f.Line,
			Description:  f.Description,
			SuggestedFix: f.SuggestedFix,
		}
	}
	return out
}

// disambiguateFindingIdentity re-hashes base (an already-computed
// ComputeFindingIdentity value shared by two or more findings in the SAME
// verdict submission, per BuildFindings' own doc comment above) together
// with line (formatted, or a fixed "noline" token when nil) and ordinal
// (this finding's own zero-based position among the colliding group, so
// even two findings sharing both content AND line still resolve to
// distinct identities rather than one silently winning). Never called for
// a base identity that occurs only once in its own batch -- BuildFindings
// is the only caller, and only inside its own counts[base] > 1 branch.
func disambiguateFindingIdentity(base string, line *int, ordinal int) string {
	lineComponent := "noline"
	if line != nil {
		lineComponent = strconv.Itoa(*line)
	}
	joined := base + findingIdentitySeparator + lineComponent + findingIdentitySeparator + strconv.Itoa(ordinal)
	sum := sha256.Sum256([]byte(joined))
	return hex.EncodeToString(sum[:])
}
