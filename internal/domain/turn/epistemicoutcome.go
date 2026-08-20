package turn

// EpistemicOutcome is the closed 3-value vocabulary the builder epistemic
// pre-action check (§20) reports on a build turn: whether the
// devil's-advocate preamble (epistemicpreamble.go) surfaced anything worth
// a second look, and how seriously. Matches the epistemic_outcome Postgres
// enum (migrations/000066_builder_epistemic_check.up.sql) exactly -- the
// SAME closed-enum discipline internal/domain/workflow.StepOutcomeStatus
// and internal/domain/review.PremiseState already establish for their own
// small, fixed vocabularies: an explicit transition/parse table below,
// never an implicit iota ordering (§11's own house style).
//
// A turn's own epistemic_outcome column is NULLABLE, and this type is
// carried behind a pointer everywhere it is actually persisted (mirrors
// sqlcgen.WorkflowStepRun.OutcomeStatus's own *WorkflowStepOutcomeStatus
// shape exactly): nil/NULL means the check never ran for this turn at all
// (feature off, a plan-mode turn per §20.3, or a build turn whose agent
// never called the reporting endpoint before the turn ended); a REAL,
// non-nil value means the check ran and reported exactly this outcome.
// §20.2 is explicit that these are two distinct facts, never collapsed:
// "a non-negotiable part of Step 61 ... without it, the false-alarm-rate
// question this feature exists to eventually answer ... is simply
// unmeasurable." EpistemicOutcomeNone ("the check ran and found nothing")
// must stay distinguishable from absent ("the check did not run") for
// exactly that reason -- the same discipline §21.1's own "'not yet
// computed' sentinel, distinct from a real zero" applies one layer up, to
// an analytics rollup: a turn that never ran the check and a turn that
// ran it and found nothing must never render identically once analytics
// reads this column back.
type EpistemicOutcome string

// The three EpistemicOutcome values (§20.1's own two-tier taxonomy, plus
// the unremarkable/no-finding case made concrete as a real, reportable
// value). See epistemicpreamble.go's own doc comment for why the
// deliberate proceed-bias behind this taxonomy is stated in the rendered
// preamble TEXT itself, not left implicit in these names/comments alone.
const (
	// EpistemicOutcomeNone is the check running and finding nothing worth
	// even a MINOR mention -- §20.1's own "silence for anything that
	// doesn't rise to either [tier]" case, reported explicitly rather
	// than left as the absence of a report (see this type's own doc
	// comment for why the distinction matters).
	EpistemicOutcomeNone EpistemicOutcome = "none"
	// EpistemicOutcomeMinor is "worth a heads-up in the reply, not worth
	// stopping for" (§20.1): the agent proceeds with the action and says
	// what it noticed.
	EpistemicOutcomeMinor EpistemicOutcome = "minor"
	// EpistemicOutcomeStrong is "worth stopping for" (§20.1): the agent
	// surfaces the concern INSTEAD OF acting and waits for the user. This
	// is a prompt-level instruction only (§20.5: a hard gate is explicitly
	// out of scope and not scheduled) -- nothing in this codebase enforces
	// that the agent actually stopped; EpistemicOutcomeStrong records what
	// the agent itself reported, nothing more.
	EpistemicOutcomeStrong EpistemicOutcome = "strong"
)

// AllEpistemicOutcomes is every recognized EpistemicOutcome, in
// declaration order -- exported so tests can range exhaustively without
// hand-maintaining a second list (mirrors workflow.AllStepOutcomeStatuses'
// identical role).
var AllEpistemicOutcomes = []EpistemicOutcome{EpistemicOutcomeNone, EpistemicOutcomeMinor, EpistemicOutcomeStrong}

// IsValidEpistemicOutcome reports whether o is one of the three
// recognized EpistemicOutcome values -- an explicit parse table, never an
// implicit iota-ordering/range check (§11's own house style; mirrors
// workflow.IsValidStepOutcomeStatus's identical precedent).
func IsValidEpistemicOutcome(o EpistemicOutcome) bool {
	switch o {
	case EpistemicOutcomeNone, EpistemicOutcomeMinor, EpistemicOutcomeStrong:
		return true
	}
	return false
}
