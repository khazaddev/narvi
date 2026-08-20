package automation

import (
	"fmt"
	"strings"
)

// BuildArtifactSummary computes §8.4's own "artifact_summary" -- a short,
// ONE-SENTENCE description of what a just-closed invocation produced
// (mockups.html's own decision 20: "artifact_summary as the first line of
// every run: what the automation produced, in one sentence"), surfaced
// onto the parent automation (migrations/
// 000055_automations_triggers_and_extras.up.sql's own artifact_summary
// column).
//
// Deliberately a MECHANICALLY-generated template over already-persisted,
// typed outcome data (total/failed run counts and the specific target
// names that failed) -- not a model-authored free-text narrative posted
// through a new agent-facing tool. internal/domain/reviewpost.Summary is
// this codebase's own established precedent for how an agent-authored
// one-line narrative is captured (a required, non-empty free-text field on
// a structured verdict-posting tool call, rendered server-side, never
// re-parsed) -- but that mechanism exists for review sessions specifically,
// which already run a dedicated verdict-posting tool call at the end of
// every session. An arbitrary automation's own turn carries no such tool
// call today, and building one (wiring a new OpenCode-facing tool +
// server-side posting endpoint an agent must be prompted to call, mirroring
// §8.2's own review-verdict machinery) would be a materially larger,
// separate feature -- out of this Step's own scope, and NOT invented here
// to avoid conflicting with a design nobody has specified for automations
// generically. This function is the honest, shippable alternative: it
// reuses data this Step ALREADY persists (automation_runs.target,
// EvaluateInvocationOutcome's own totalRuns/failedRuns counts) rather than
// leaving artifact_summary permanently NULL, the exact "column exists in
// the UI but the backend never fills it" gap mockups.html's own decision 20
// names as the classic failure mode this exists to close.
//
// totalRuns/failedRuns are the SAME counts EvaluateInvocationOutcome
// already consumes; failedTargetNames is the ordered list of
// automation_runs.target names (this package's own Target.Name) whose run
// reached RunStatusFailed for this invocation -- the caller (app/
// automation's own closeout.go) derives all three from data it already
// queries to decide the invocation's own outcome, no new query beyond
// naming which targets failed.
func BuildArtifactSummary(totalRuns, failedRuns int, failedTargetNames []string) string {
	if failedRuns == 0 {
		if totalRuns == 1 {
			return "Succeeded: 1/1 target."
		}
		return fmt.Sprintf("Succeeded: %d/%d targets.", totalRuns, totalRuns)
	}

	succeeded := totalRuns - failedRuns
	names := "unknown target"
	if len(failedTargetNames) > 0 {
		names = strings.Join(failedTargetNames, ", ")
	}
	return fmt.Sprintf("Failed: %d/%d targets failed (%s); %d succeeded.", failedRuns, totalRuns, names, succeeded)
}
