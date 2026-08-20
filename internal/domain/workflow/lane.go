package workflow

import "github.com/khazaddev/narvi/internal/domain/intent"

// Lane is one of the three dispatch lanes a workflow definition serves --
// a closed enum matching the workflow_lane Postgres enum
// (migrations/000057_workflows.up.sql) exactly, by value, for equality
// purposes only (this package never imports the generated sqlcgen type
// itself, mirroring internal/domain/plan.Status's own identical
// convention).
type Lane string

// The three Lane values (§25.4: "a closed enum (review/request/plan)").
// Deliberately a CLOSED set: §25.4's whole model -- three system
// workflows, per-lane bindings, the canvas editor's own closed-model
// validation (§25.12) -- depends on a lane being one of exactly these
// three, never an open vocabulary.
const (
	LaneReview  Lane = "review"
	LaneRequest Lane = "request"
	LanePlan    Lane = "plan"
)

// AllLanes is every recognized Lane, in declaration order -- exported so
// tests (and the seed-row assertions in
// internal/adapters/outbound/postgres's integration suite) can range
// exhaustively without hand-maintaining a second list, mirroring
// authz.AllActions/providercredential.AllProviders.
var AllLanes = []Lane{LaneReview, LaneRequest, LanePlan}

// IsValidLane reports whether l is one of the three recognized Lane
// values.
func IsValidLane(l Lane) bool {
	switch l {
	case LaneReview, LaneRequest, LanePlan:
		return true
	}
	return false
}

// LaneFor maps the intent classifier's own existing (target, mode)
// vocabulary (internal/domain/intent/rubric.go) onto a Lane -- §25.4: "a
// pure mapping over the classifier's own existing vocabulary ... not a
// new vocabulary invented alongside it". Total: it NEVER returns an
// error or an unrecognized Lane, per §25.13's own fail-open requirement
// ("LaneFor must default the same way rather than block dispatch on an
// unresolved lane" -- the same discipline as the classifier's own
// IsActive defaulting every unconfigured surface to shadow, §18.5).
//
// The mapping, with each judgment call documented:
//
//   - intent.TargetReview -> LaneReview.
//   - intent.TargetRelease / intent.TargetFeature -> LaneReview. These
//     two are §15's release-vs-feature category (§15.1, §18.6) --
//     per rubric.go's own doc comment, that category distinguishes "a
//     release PR review" from "an ordinary feature/fix PR review":
//     BOTH values describe a review-lane job, refining WHICH review
//     prompt/manifest treatment applies (§15), never routing work out
//     of the review lane. §25.4 keeps Lane a closed 3-value enum, so
//     these refine within LaneReview rather than adding a fourth lane.
//   - For every review-family target above, mode is ignored entirely: a
//     review session has no plan/build split anywhere in this codebase
//     (plan mode is a request-lane concept, §8.1), so there is nothing
//     for mode to select.
//   - intent.TargetRequest with intent.ModePlan -> LanePlan; with
//     intent.ModeBuild -> LaneRequest.
//   - Anything unrecognized (a foreign/empty target, a foreign/empty
//     mode) falls open into the same request-vs-plan branch keyed ONLY
//     on mode: intent.ModePlan -> LanePlan (plan mode's own
//     deterministic boolean is a signal sessions already carry today,
//     honored even when the target is garbage), anything else ->
//     LaneRequest -- the passthrough lane whose built-in workflow
//     reproduces today's exact dispatch behavior (§25.8), so an
//     unresolved classification degrades to "what would have happened
//     anyway", never a blocked dispatch.
func LaneFor(target, mode string) Lane {
	switch target {
	case intent.TargetReview, intent.TargetRelease, intent.TargetFeature:
		return LaneReview
	}
	if mode == intent.ModePlan {
		return LanePlan
	}
	return LaneRequest
}
