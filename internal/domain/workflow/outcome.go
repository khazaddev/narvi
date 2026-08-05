package workflow

// StepOutcomeStatus is the closed 3-value vocabulary a finished step
// reports and the ONLY axis an Edge may condition on (§25.4) -- matches
// the workflow_step_outcome_status Postgres enum
// (migrations/000057_workflows.up.sql) exactly. The same closed-enum
// discipline internal/domain/review.Shippable already establishes for
// its own 3-value enum -- and deliberately a DISTINCT axis from
// Shippable (§25.4/§25.8): the review lane's Shippable verdict is
// consumed after its single step completes by the existing auto-approval
// machinery, never routed through this vocabulary, and an edge never
// branches on it.
type StepOutcomeStatus string

// The three StepOutcomeStatus values. Their default (no-explicit-edge)
// consequences are fail-conservative (§25.4): only StepOutcomeOK
// advances on its own; the other two escalate unless a retry edge was
// wired explicitly.
const (
	// StepOutcomeOK is the step finishing with nothing further needed
	// from a retry/escalation point of view -- with no explicit edge, the
	// run advances to the next step in Order (or completes at the last).
	StepOutcomeOK StepOutcomeStatus = "ok"
	// StepOutcomeNeedsFix is the step reporting concrete, fixable
	// problems (§25.9's audit step is the canonical producer) -- with no
	// explicit edge, the run escalates; a bounded auto-fix loop must be
	// wired explicitly (Edge{audit, needs_fix, fix}), never implied.
	StepOutcomeNeedsFix StepOutcomeStatus = "needs_fix"
	// StepOutcomeBlocked is the step reporting it cannot make progress
	// at all -- with no explicit edge, the run escalates.
	StepOutcomeBlocked StepOutcomeStatus = "blocked"
)

// AllStepOutcomeStatuses is every recognized StepOutcomeStatus, in
// declaration order -- exported so tests can range exhaustively without
// hand-maintaining a second list.
var AllStepOutcomeStatuses = []StepOutcomeStatus{StepOutcomeOK, StepOutcomeNeedsFix, StepOutcomeBlocked}

// IsValidStepOutcomeStatus reports whether s is one of the three
// recognized StepOutcomeStatus values.
func IsValidStepOutcomeStatus(s StepOutcomeStatus) bool {
	switch s {
	case StepOutcomeOK, StepOutcomeNeedsFix, StepOutcomeBlocked:
		return true
	}
	return false
}
