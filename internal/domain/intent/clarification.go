package intent

// DeriveNeedsClarification derives whether an ingress surface should pause
// and ask a human to disambiguate rather than act on a classifier
// decision, from confidence plus how many plausible targets the input
// supports -- never asked of the model directly (§18.2: "keeping the
// threshold a versionable, testable piece of code rather than model
// behavior").
//
// plausibleTargetCount is the number of distinct readings the calling
// surface judges the input could plausibly support (a caller unable to
// estimate this should pass 2 -- "could be either" -- the conservative
// default that never suppresses a clarification a genuinely ambiguous
// input deserves).
//
// The policy, in order:
//   - confidence == low: a low-confidence classification means the model
//     itself found no strong textual signal (§18.2's own "low" rubric
//     entry) -- always ask, regardless of plausibleTargetCount, since a
//     low reading is already evidence of ambiguity.
//   - confidence == medium: ask ONLY when more than one target is
//     genuinely plausible (plausibleTargetCount > 1) -- a medium-
//     confidence reading with only one plausible target is a reasonable
//     inference worth acting on, not a coin flip.
//   - confidence == high: never ask -- §18.2's own "high" rubric entry is
//     "an attentive reader would not second-guess", so a high-confidence
//     reading is acted on even when a naive count would list more than
//     one nominally "plausible" target.
//   - any other/unrecognized confidence value: treated exactly like low
//     (the safe default -- an unrecognized value is never treated as
//     confidently unambiguous).
func DeriveNeedsClarification(confidence string, plausibleTargetCount int) bool {
	switch confidence {
	case ConfidenceHigh:
		return false
	case ConfidenceMedium:
		return plausibleTargetCount > 1
	default:
		// ConfidenceLow, or any unrecognized value.
		return true
	}
}
