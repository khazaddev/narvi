package intent

// CorroborateTarget implements §18.2's mandatory independent-deterministic-
// check requirement: "For any IRREVERSIBLE action (triggering a review,
// dispatching a build), the classifier's signal MUST be corroborated by
// an independent deterministic check (a regex or label match) before
// acting; on disagreement between the two, ask for clarification rather
// than guessing."
//
// classifierTarget is the LLM-derived Target (e.g. intent.TargetReview /
// intent.TargetRequest). deterministicTarget is an independently-computed,
// non-LLM signal a caller already has on hand (a regex match, a label, an
// architectural fact like "this mention landed on an already-tracked PR
// row") -- empty string means "no deterministic signal available for this
// input". irreversible reports whether finalTarget is about to drive an
// irreversible action for THIS call; when false, corroboration is not
// required at all and classifierTarget passes through unchanged.
//
// Returns finalTarget and corroborated (true iff a deterministic signal
// existed AND it agreed with the classifier -- i.e. corroboration
// actually succeeded, so a caller can safely act without asking):
//
//   - !irreversible: always (classifierTarget, true) -- nothing to
//     corroborate, the action isn't irreversible in the first place.
//   - irreversible, deterministicTarget == "": (classifierTarget, false)
//     -- no independent signal exists to corroborate against at all; the
//     classifier's own guess is still returned as the best available
//     value, but corroborated=false tells the caller it was never
//     actually verified.
//   - irreversible, both signals agree: (classifierTarget, true).
//   - irreversible, both signals disagree: (deterministicTarget, false)
//     -- the verifiable, non-LLM signal is returned as the safer of the
//     two disagreeing values, but corroborated=false means a caller MUST
//     still ask for clarification rather than act on it silently (§18.2:
//     "ask for clarification rather than guessing" -- never resolved by
//     picking a winner and proceeding).
func CorroborateTarget(classifierTarget, deterministicTarget string, irreversible bool) (finalTarget string, corroborated bool) {
	if !irreversible {
		return classifierTarget, true
	}
	if deterministicTarget == "" {
		return classifierTarget, false
	}
	if classifierTarget == deterministicTarget {
		return classifierTarget, true
	}
	return deterministicTarget, false
}
