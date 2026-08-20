package intent

// ConfidenceRubric is §18.2's confidence rubric, verbatim, as a single
// shared constant every ingress surface's classification call reuses. It
// belongs at the FIELD-DESCRIPTION level of the classifier's structured-
// output JSON schema (the confidence field's own schema description),
// never floated separately in a system prompt or duplicated per surface
// -- internal/app/intentclassifier's schema-building code references this
// constant directly rather than restating the text.
//
// Anchored on how DIRECTLY the input text supports the decision, never on
// how certain the model reports feeling -- a rubric asking the latter
// degrades to reporting "high" almost unconditionally (§18.2).
const ConfidenceRubric = `Confidence must reflect how directly the input text supports this decision -- never how certain you feel. Use exactly one of:
- "high": a clear, direct textual signal (even a well-known synonym) that an attentive reader would not second-guess.
- "medium": a reasonable inference from context, tone, or indirect phrasing that an attentive reader could plausibly read differently.
- "low": no strong signal; the input plausibly supports more than one reading.`

// The three confidence values ConfidenceRubric governs. Plain string
// constants (not a distinct named type) so they interoperate directly
// with ports.IntentDecision.Confidence, whose own fixed contract (§18.1)
// types that field as a bare string.
const (
	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"
)

// The two Target values this Step's classifier distinguishes (§18: unified
// intent classifier does "review-vs-request ... across all ingress
// surfaces"). §18.1 leaves Target's exact vocabulary as "decision-
// specific, e.g. review/request" -- these two are this codebase's own
// concrete choice, matching the phrasing used throughout §18 itself.
const (
	TargetReview  = "review"
	TargetRequest = "request"
)

// The two Mode values this Step's classifier distinguishes (§18: "plan-
// vs-build across all ingress surfaces") -- matching the existing,
// already-consumed restdtos.CreateSessionRequest.PlanMode /
// sessionactor.dispatch.go target.PlanMode boolean exactly (Mode ==
// ModeBuild is the false/default PlanMode value, Mode == ModePlan is
// true).
const (
	ModePlan  = "plan"
	ModeBuild = "build"
)

// The two Target values §15's own release-vs-feature category
// distinguishes (§15.1, §18.6: "release-vs-feature is just one more
// category alongside review-vs-request and plan-vs-build ... through the
// same contract, rubric, and record shape"). A release PR review (§15) is
// a materially different job from an ordinary feature/fix PR review
// (§15's own opening paragraph) -- these two values are this category's
// own concrete Target vocabulary, exactly like TargetReview/TargetRequest
// above are review-vs-request's.
const (
	TargetRelease = "release"
	TargetFeature = "feature"
)
