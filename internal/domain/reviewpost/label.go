package reviewpost

import "github.com/khazaddev/narvi/internal/domain/review"

// The review:*-risk label vocabulary this Step's label sync ever adds or
// removes, plus LabelNeedsHuman, which it never does. Exact strings match
// docs/design/mockups.html's own literal "labels synced: review:medium-
// risk" text (verdict-foot, the code-review view) -- the one place the
// mockup shows a REAL, raw GitHub label string rather than the UI's own
// human-facing "review: medium risk" chip text (space after the colon,
// for display only).
const (
	LabelLowRisk    = "review:low-risk"
	LabelMediumRisk = "review:medium-risk"
	LabelHighRisk   = "review:high-risk"

	// LabelNeedsHuman is §21.2's own escape hatch -- the OLD "review: low
	// risk" auto-approval-trigger label INVERTED (§8.2): a MAINTAINER
	// applies this label, by hand, to force a
	// specific PR out of auto-approval regardless of what criteria say
	// (§21.2, §8.6's own eligibility engine). ComputeLabelSync below
	// NEVER adds or removes this label -- it is deliberately excluded from
	// riskLabels, the only set that function ever touches. Bot-written
	// status and human-issued command must never share write ownership of
	// the same label (§5.1's own general principle, and internal/platform/
	// config.go's gitHubReReviewLabelEnvVarName doc comment draws this
	// exact same distinction for the SEPARATE re-trigger label): if
	// ComputeLabelSync ever silently removed this label because a later
	// review computed a lower risk, it would erase a maintainer's own
	// deliberate override without their asking -- exactly the hazard this
	// separation prevents.
	LabelNeedsHuman = "review:needs-human"
)

// riskLabels is every label ComputeLabelSync is willing to add OR remove
// -- LabelNeedsHuman is deliberately absent, see its own doc comment
// above.
var riskLabels = []string{LabelLowRisk, LabelMediumRisk, LabelHighRisk}

// RiskLabel maps risk to the ONE review:*-risk label that reflects it.
// The zero value or any other unrecognized RiskLevel fails conservative
// to LabelHighRisk -- mirrors review.baselineFromRisk's own identical
// "unrecognized ranks with RiskLevelHigh" policy (review/doc.go's uniform
// fail-conservative rule): an unassessed/garbled risk must never display
// as the LEAST alarming label.
func RiskLabel(risk review.RiskLevel) string {
	switch risk {
	case review.RiskLevelLow:
		return LabelLowRisk
	case review.RiskLevelMedium:
		return LabelMediumRisk
	default:
		// review.RiskLevelHigh, or any unrecognized value.
		return LabelHighRisk
	}
}

// LabelSyncPlan is ComputeLabelSync's own return shape: the label to add
// (empty when the desired label is already present) and the OTHER
// risk-tier labels to remove (empty when none of them are currently
// present) -- a caller applies Add then Remove against the real PR,
// leaving exactly one of the three riskLabels present and LabelNeedsHuman
// completely untouched either way.
type LabelSyncPlan struct {
	Add    []string
	Remove []string
}

// ComputeLabelSync computes the label-sync plan for a PR currently
// carrying currentLabels (its own real, live GitHub labels -- the caller
// fetches these; this function stays pure by taking them as an input
// rather than fetching them itself), given the verdict's own RiskLevel.
// Idempotent: calling this again with the SAME currentLabels/risk (e.g. a
// re-review that reaches the identical risk assessment) produces an empty
// plan (nothing to add, nothing to remove) once the first sync already
// applied it.
func ComputeLabelSync(currentLabels []string, risk review.RiskLevel) LabelSyncPlan {
	desired := RiskLabel(risk)

	present := make(map[string]bool, len(currentLabels))
	for _, l := range currentLabels {
		present[l] = true
	}

	var plan LabelSyncPlan
	if !present[desired] {
		plan.Add = append(plan.Add, desired)
	}
	for _, l := range riskLabels {
		if l != desired && present[l] {
			plan.Remove = append(plan.Remove, l)
		}
	}
	return plan
}
