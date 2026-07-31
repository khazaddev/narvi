package review

// Shippable is the automated-approval engine's own gating field (§21.2, a
// later Step): auto/needs_human/block. This is the AUTHORITATIVE,
// server-computed classification — the ONLY sanctioned way to produce a
// legitimate value is ComputeShippable's return value. See doc.go's
// "server-computed Shippable" section and ProposedShippable's own doc
// comment below for why this is a distinct type from the model's own
// self-report, never interchangeable with it without an explicit
// conversion.
type Shippable string

// The three Shippable values, ordered auto < needs_human < block (most to
// least permissive) — see shippableRank below for the explicit total
// order, and doc.go's "ranking is an explicit table" section for why that
// order is never inferred from these consts' declaration position.
const (
	// ShippableAuto is eligible for the automated-approval eligibility
	// engine (§21.2) to consider — NOT itself sufficient for auto-merge;
	// §21.2 layers CI-green/diff-size/sensitive-path/verdict-freshness
	// criteria on top of Shippable == auto.
	ShippableAuto Shippable = "auto"
	// ShippableNeedsHuman requires a person to review before the PR can
	// proceed; excluded from auto-approval outright.
	ShippableNeedsHuman Shippable = "needs_human"
	// ShippableBlock is this package's strictest classification —
	// reserved for premise-level illegitimacy (PremiseStateNotAPR) or any
	// unrecognized enum value anywhere in this package's fail-conservative
	// policy (doc.go). See RiskLevel's own doc comment for why RiskLevel
	// alone, even at its own highest defined tier, never reaches this
	// value on its own.
	ShippableBlock Shippable = "block"
)

// ProposedShippable is the model's OWN self-report of what it believes
// Shippable should be — carried on Verdict purely for audit/transparency
// (§21.2: "the LLM's verdict only ever *proposes* Shippable"). It is a
// DISTINCT Go type from Shippable, not merely a different field name: a
// ProposedShippable value cannot be assigned into a Shippable-typed field
// or parameter (Verdict.Shippable, any argument of ComputeShippable)
// without an explicit type conversion, so accidentally laundering a
// model's guess into the authoritative field is a visible, grep-able act,
// never a silent one. ComputeShippable's own signature does not accept a
// ProposedShippable parameter at all — the model's opinion is not merely
// distrusted, it is structurally unable to influence the computation.
//
// The three ProposedShippable values share Shippable's own underlying
// strings (auto/needs_human/block) deliberately — the model is proposing
// the same three-way classification, just via a type the compiler will
// never treat as equivalent to the authoritative one. Nothing in this
// package validates, ranks, or otherwise interprets a ProposedShippable
// value; it is opaque data as far as ComputeShippable is concerned.
type ProposedShippable string

// The three ProposedShippable values, mirroring Shippable's own three
// strings (see the type's own doc comment for why the values match but the
// type does not).
const (
	ProposedShippableAuto       ProposedShippable = "auto"
	ProposedShippableNeedsHuman ProposedShippable = "needs_human"
	ProposedShippableBlock      ProposedShippable = "block"
)

// shippableRank is the explicit total order over Shippable, from most to
// least permissive: auto(0) < needs_human(1) < block(2). Deliberately NOT
// derived from iota/declaration order on the Shippable consts above (see
// doc.go's "ranking is an explicit table" section) — a reorder of those
// consts (e.g. an alphabetizing pass, or a new value inserted between two
// existing ones) can never silently invert the policy this table encodes,
// because nothing in this package ever compares the consts' own
// declaration position; every comparison goes through rank() below.
var shippableRank = map[Shippable]int{
	ShippableAuto:       0,
	ShippableNeedsHuman: 1,
	ShippableBlock:      2,
}

// rank returns s's position in shippableRank's explicit total order. Every
// producer in this package (baselineFromRisk, CoverageFloor, PremiseFloor)
// only ever returns one of the three legal Shippable consts, so this
// branch is never exercised by a legitimate call within this package
// today — it exists purely as defense in depth, so that IF a future change
// ever pipes an unvalidated Shippable value through rank() (rather than
// through one of the three producer functions above), the result fails
// conservative: ranked one above the strictest legal value (ShippableBlock
// itself), never silently read as rank 0 ("auto") via a bare map-lookup
// miss. maxShippable below never reconstructs a NEW Shippable value from a
// rank number — it always returns one of its two INPUT values verbatim —
// so this sentinel rank can never itself leak out as a fabricated
// Shippable string.
func rank(s Shippable) int {
	if r, ok := shippableRank[s]; ok {
		return r
	}
	return len(shippableRank)
}

// maxShippable returns whichever of a, b ranks least permissive (highest
// rank), preferring b on a tie (a bare `>` on a's rank, not `>=`) — the
// specific tie-break is immaterial here since ComputeShippable only ever
// calls this with two values it expects MAY be equal, never relying on
// which one "wins" a tie to distinguish behavior.
func maxShippable(a, b Shippable) Shippable {
	if rank(a) > rank(b) {
		return a
	}
	return b
}

// baselineFromRisk maps the reviewer's own overall RiskLevel to the
// Shippable value that would apply BEFORE either floor is considered —
// i.e. what Shippable would be if coverage and premise were both
// perfectly clean. RiskLevel is not itself one of the two named
// raise-only floors (only coverage and premise are, per §8.2/Step 45) —
// see doc.go's design call #2 for why it is instead treated as the
// baseline the two floors can only ever raise, never lower, keeping
// "raise-only" uniform across all three inputs to ComputeShippable.
//
//   - RiskLevelLow: ShippableAuto — the reviewer found nothing that
//     itself warrants human gating; only a floor can raise this further.
//   - RiskLevelMedium, RiskLevelHigh: ShippableNeedsHuman — a person
//     should look, but nothing here alone means the PR must be blocked
//     outright (see RiskLevel's own doc comment for why a three-tier
//     scale never reaches Block on its own).
//   - the zero value or any other unrecognized RiskLevel:
//     ShippableNeedsHuman, FAILING CONSERVATIVE — ranked identically to
//     RiskLevelHigh, this enum's own worst known legitimate value (doc.go's
//     uniform fail-conservative policy), never as permissive as
//     RiskLevelLow's ShippableAuto.
func baselineFromRisk(r RiskLevel) Shippable {
	switch r {
	case RiskLevelLow:
		return ShippableAuto
	case RiskLevelMedium, RiskLevelHigh:
		return ShippableNeedsHuman
	default:
		return ShippableNeedsHuman
	}
}

// ComputeShippable is domain/review's single exported pure function for
// deriving Shippable (§8.2/Step 45) — the ONLY sanctioned way any caller
// computes an authoritative Shippable value. It is a pure function of the
// reviewer's own RiskLevel plus the two independent raise-only floors
// (coverage, premise), composed via max(rank):
//
//	result = max(rank(baselineFromRisk(risk)),
//	             rank(CoverageFloor(coverage)),
//	             rank(PremiseFloor(premise)))
//
// Deliberately NOT a parameter here: any model-proposed value
// (ProposedShippable). This is not an oversight — it is the whole point of
// §8.2/Step 45's "Shippable is server-computed, never the model's
// self-report" rule (doc.go's own top-level section): the model's own
// guess is structurally incapable of influencing this function's result,
// because it is not a parameter this signature even accepts. A caller
// wiring up a Verdict (a later Step) is expected to populate
// Verdict.Shippable with EXACTLY this function's return value and never
// with Verdict's own ProposedShippable field, converted or otherwise — see
// Verdict's own doc comment (verdict.go).
//
// RAISE-ONLY property: for any (risk, coverage, premise) input, this
// function never returns a Shippable ranked BELOW baselineFromRisk(risk)
// alone, nor below CoverageFloor(coverage) alone, nor below
// PremiseFloor(premise) alone — max() is monotonic in each argument by
// construction, and every producer above returns one of exactly three
// legal Shippable values, so this composition can never observe (let
// alone propagate) an out-of-band rank. See
// TestComputeShippable_RaiseOnly (shippable_test.go) for this property
// proved exhaustively across the full input matrix.
func ComputeShippable(risk RiskLevel, coverage TestsCoverageState, premise PremiseState) Shippable {
	result := baselineFromRisk(risk)
	result = maxShippable(result, CoverageFloor(coverage))
	result = maxShippable(result, PremiseFloor(premise))
	return result
}
