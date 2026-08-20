package intent

// This file (planfollowup.go) implements §23's own plan_followup
// classification category (§23.1: "amend-vs-answer... a new surface on
// the existing unified intent classifier ... alongside the classifier's
// other categories (review-vs-request, plan-vs-build, release-vs-feature,
// §18.6)"). Mirrors release.go's own precedent of a dedicated per-Step
// file for a single classification category's own domain-level pieces,
// one level up from rubric.go's shared, cross-category constants.
//
// Problem this answers: the interim server-side gate shipped by Steps
// 37/38's own follow-up fix (internal/adapters/inbound/httpapi's
// createTurnLocked, turn.go) holds ANY plan-mode=false reply while a
// plan is awaiting_approval, UNLESS it starts with the deterministic
// "revise:" prefix (plandomain.RevisePrefix, verdict.go) -- that file's
// own doc comment names this Step directly: "a future Step is expected
// to replace prefix-detection with a real amend-vs-answer LLM classifier
// for the common case, with RevisePrefix remaining available afterward
// as a deterministic fallback a user can always reach for." This file is
// that classifier's own pure decision layer.

// The two Target values plan_followup's own classification category
// distinguishes (§23.1) -- exactly like TargetReview/TargetRequest
// (review-vs-request) and TargetRelease/TargetFeature (release-vs-
// feature) are each category's own concrete Target vocabulary,
// rubric.go's own "decision-specific, e.g. review/request" contract
// (§18.1's ports.IntentDecision.Target doc comment).
const (
	// TargetAmend means the reply is requesting a change to the plan --
	// treated EXACTLY like a plandomain.RevisePrefix-prefixed reply
	// (§23 intro: "replaces the prefix requirement with a real
	// amend-vs-answer classification").
	TargetAmend = "amend"
	// TargetAnswer means the reply does NOT request a plan change --
	// held for clarification exactly like any other reply that matches
	// neither a plan verdict nor plandomain.RevisePrefix today.
	TargetAnswer = "answer"
)

// ResolveAnswerOnly derives §23's own persisted turns.answer_only flag
// (§23.2) from ONE plan_followup classification's raw source/target/
// confidence -- pure, so this fail-open policy (§23.3) is a single,
// versionable, unit-testable decision function, mirroring
// DeriveNeedsClarification's own identical "the threshold is a
// versionable, testable piece of code, never asked of the model
// directly" rationale (§18.2), rather than logic inlined at
// intentclassifier.Service's own one real call site
// (ClassifyPlanFollowup) or at httpapi.createTurnLocked's own call site.
//
// source/target/confidence are ports.IntentDecision.Source/.Target/.
// Confidence's own bare string VALUES -- this package cannot import
// internal/app/ports (§11's domain/no-outward-dependency rule: /internal/
// domain never imports /internal/app). record.go's own RecordSourceClassifier
// is the identical, already-established precedent for duplicating the
// SAME string values locally rather than importing them; callers pass
// ports.IntentDecision's own fields straight through, unconverted (the
// underlying string values are identical either way).
//
// Fails open toward true ("answer" -- hold for clarification, §23.3's own
// floor: "nothing is dispatched, the plan stays awaiting_approval") in
// EVERY case except a genuinely confident "amend" verdict:
//
//   - source != RecordSourceClassifier (a fallback, ANY FallbackReason) ->
//     true. §23.3: "A classifier failure must never let a build turn
//     dispatch against an unapproved plan, under any failure mode."
//   - source == RecordSourceClassifier but DeriveNeedsClarification(confidence,
//     2) reports true: plan_followup has no independent deterministic
//     signal of its own to narrow "how many plausible readings" (unlike
//     the irreversible-action corroboration §18.2 describes for
//     review-vs-request), so the conservative plausibleTargetCount=2
//     ("could be either") default is used unconditionally -- a "low"
//     confidence always asks, and a "medium" one always asks too (2 > 1)
//     -> true.
//   - source == RecordSourceClassifier, confident (DeriveNeedsClarification
//     false, i.e. Confidence == high), target != TargetAmend (== TargetAnswer,
//     or any other/unrecognized value) -> true.
//   - source == RecordSourceClassifier, confident, target == TargetAmend ->
//     false. The ONLY case that unblocks dispatch.
func ResolveAnswerOnly(source, target, confidence string) bool {
	if source != RecordSourceClassifier {
		return true
	}
	if DeriveNeedsClarification(confidence, 2) {
		return true
	}
	return target != TargetAmend
}
