package reviewtriage

// CostBudget is §26.7's own per-repo-tunable per-path cost ceiling
// (Step 69, "per-review cost budget with look-ahead"): "reviewCostBudget:
// {light: <usd>, deep: <usd>} joins §26.3's reviewDepth config on the SAME
// per-repo settings row". Zero (the Go zero value for both fields) means
// "no ceiling configured for this path" -- ShouldSkipOptionalPass below
// treats a zero ceiling as "never skip" (see that function's own doc
// comment), never as "skip everything", so an unconfigured repo behaves
// exactly like today: every optional pass always dispatches.
type CostBudget struct {
	// Light is the light path's own ceiling in USD -- a degenerate,
	// one-checkpoint case (§26.7: "the one optional pass it can run at
	// all is §26.6's fact-check sub-task").
	Light float64
	// Deep is the deep path's own ceiling in USD -- checked once before
	// each of up to three optional sub-tasks (architecture-scribe,
	// fact-check, counter-reviewer), in whatever order the primary
	// reviewer's own orchestration dispatches them (§26.7).
	Deep float64
}

// ForDepth returns b's own ceiling for depth -- DepthDeep selects Deep,
// everything else (DepthLight, the zero value, or any unrecognized
// ReviewDepth) selects Light. This is deliberately NOT the SAME fail-
// conservative-toward-the-worst-case policy every closed enum elsewhere in
// this package's sibling review package follows for an unrecognized
// value: an unrecognized/unresolved depth here means "we don't know this
// was routed deep", which is honestly indistinguishable from "it wasn't"
// -- the identical reasoning rank (depth.go) already states for Floor's
// own prior-depth handling, applied here to which BUDGET a caller reads
// rather than which depth composition wins.
func (b CostBudget) ForDepth(depth ReviewDepth) float64 {
	if depth == DepthDeep {
		return b.Deep
	}
	return b.Light
}

// DefaultCostBudget is this Step's own proposed starting figures (§26.7:
// "propose $0.50 light / $5 deep per review, matching this plan's own
// convention of proposing a concrete, explicitly-tunable starting figure
// rather than leaving a blank" -- the SAME precedent §24.6's own
// auto_retrigger_count budget of 10 already set). Applied whenever a repo
// has never configured reviewCostBudget at all -- internal/app/
// reviewtriage.LoadConfig's own doc comment: a missing repo_settings row,
// or a NULL review_cost_budget_light_usd/review_cost_budget_deep_usd
// column on an existing one, both resolve to this -- mirroring
// DefaultConfig's own identical "never configured yet" precedent
// (config.go).
func DefaultCostBudget() CostBudget {
	return CostBudget{Light: 0.50, Deep: 5.00}
}

// CostBudgetSafetyMargin is §26.7's own proposed look-ahead safety margin
// -- "propose 80%, mirroring OpenCodeReview's own 4/5 figure" -- expressed
// as the FRACTION of the ceiling that must remain UNSPENT for another
// optional pass to still be worth dispatching (i.e. dispatch is allowed
// while spent <= ceiling * 0.8). A named, EXPORTED constant (B5 fix --
// previously unexported and duplicated as a hardcoded "a rough 80%
// margin" English literal in internal/domain/review/context.go's own
// subAgentOrchestrationInstructions, the prompt text a reviewing agent
// actually reads: changing this constant would have silently
// desynchronized that prose from the real figure, since review's own
// "zero external imports" convention (doc.go) forbids that package from
// importing this one to read the constant directly -- see
// review.PreFetchedContext.CostBudgetSafetyMarginPercent's own doc
// comment for how the value reaches that prompt text instead, threaded in
// by a caller that already imports both packages, exactly like
// ReviewCostBudgetUSD itself already is) -- so a future per-repo override
// of the margin itself (not asked for by this Step) would have exactly
// one call site to change, and the prompt text an agent reads can never
// drift from it.
const CostBudgetSafetyMargin = 0.8

// ShouldSkipOptionalPass is §26.7's own "Mechanism" -- the ONE exported
// pure function checking accumulated spend against a per-path ceiling
// BEFORE the primary reviewer's orchestration would dispatch the NEXT
// optional sub-task (architecture-scribe, counter-reviewer, or §26.6's
// fact-check sub-task): "checks that running total against a per-path
// ceiling at a safety margin ... and skips the dispatch if already at or
// over it". This is deliberately NOT a prediction of what the next pass
// would itself cost (§26.7: "unknowable in advance ... it is a ceiling
// enforced BEFORE commitment") -- spentUSD is whatever has ALREADY been
// spent on this review so far (§7.1's own cost roll-up: main lane plus
// every sub-task already run), and ceilingUSD is the resolved CostBudget
// field for this review's own path (Light or Deep, the caller's own
// choice of which).
//
// A zero ceilingUSD (CostBudget's own "no ceiling configured" zero value)
// NEVER skips -- there is nothing to check accumulated spend against, and
// treating an unconfigured budget as an implicit $0 ceiling would silently
// skip every optional pass on every review for a repo that never opted
// into this feature at all, the exact "subtract rigor from the default"
// this Step's own invariant (§26.9) forbids. A negative spentUSD or
// ceilingUSD (should never occur from a real caller -- a cost roll-up or a
// configured ceiling is never negative) is treated exactly like every
// other domain package's fail-conservative policy for a value outside its
// own legitimate range: negative ceilingUSD degrades to "no ceiling
// configured" (same as zero, never skip -- there is no principled meaning
// to "a negative budget" this function could enforce); negative spentUSD
// is clamped to 0 (never accidentally computed as "under budget by MORE
// than the whole ceiling", which a bare negative-spend subtraction could
// produce).
//
// RAISE-ONLY in the sense THIS package's other decision functions are:
// this function only ever recommends skipping MORE conservatively as
// spend rises toward the ceiling, never the reverse.
//
// NOT YET CALLED BY ANY PRODUCTION PATH (B5 disclosure): §26.7's own
// enforcement mechanism is the reviewing agent's OWN self-governed
// judgment against the dollar ceiling stated in its prompt (review/
// context.go's own subAgentOrchestrationInstructions -- see that
// function's own "a self-governed, best-effort check, not a server-
// enforced gate" doc comment for the full "why": this control plane has
// no channel to intervene inside an already-dispatched turn at all).
// This function is that same policy's REFERENCE implementation --
// grepped for callers before writing this disclosure, and there are
// none outside this package's own tests -- kept as a real, tested,
// exported pure function against exactly the day a server-side
// verification/audit path is built on top of it, but nothing calls it
// today. A doc comment silent on this would read as though this ceiling
// were actively, mechanically enforced somewhere; it is not, yet.
func ShouldSkipOptionalPass(spentUSD, ceilingUSD float64) bool {
	if ceilingUSD <= 0 {
		return false
	}
	if spentUSD < 0 {
		spentUSD = 0
	}
	return spentUSD >= ceilingUSD*CostBudgetSafetyMargin
}
