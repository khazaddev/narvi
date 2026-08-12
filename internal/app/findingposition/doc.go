// Package findingposition implements §22.1.1's own relocation fallback
// (Step 63, "review: learned false-positive patterns"): when internal/
// domain/reviewpost.MatchPosition's pure sliding-window match fails to
// anchor a finding, this package makes ONE small, structured, NON-AGENTIC
// call through the existing ports.LLM port (§4.3) -- the same
// multi-provider-by-construction port internal/app/intentclassifier
// already requires (§18.1) -- to ask, one finding at a time, "where in
// this diff (if anywhere) does this finding's own snippet now live".
//
// # Why this mirrors intentclassifier, not a new call class
//
// §22.1.1 is explicit: "a utility call with no tool access, the same
// class of call as classification, not review" -- not a review-session
// turn, not §7.1's sub-task fan-out. This package's own Resolver.Resolve
// therefore mirrors intentclassifier.Service.Classify's shape file-for-
// file: a structured-output ports.LLM.Complete call constrained by a
// small, fixed JSON Schema (schema.go), parsed defensively (never
// trusting a provider to honor its own schema blindly), and a
// never-caller-fatal contract -- ANY failure (a *ports.LLMError of any
// Code, a schema violation, a nonsensical/out-of-range line range) lands
// on (0, 0), exactly like a failed pure match does, never a second guess
// stacked on the first (§22.1.1: "the finding stays unanchored (0, per
// above), never a second guess stacked on the first"). No tool access:
// ports.LLM.Complete has no tool-calling concept at all -- it is a pure
// structured-output text completion, so "non-agentic" holds by
// construction of the port itself, not merely by this package's own
// restraint.
//
// # File layout
//   - schema.go: the fixed response schema Complete's structured output
//     is constrained to, and the wire shape it parses into.
//   - resolver.go: Resolver, Resolve -- the one relocation call.
//   - resolveall.go: ResolveAll -- the batch orchestration httpapi.
//     PostReviewVerdict calls: attempts reviewpost.MatchPosition first for
//     every finding, invoking Resolve only for the ones that failed (and
//     only when a diff is actually in hand -- see that function's own doc
//     comment for the full "no diff, no call" short-circuit).
package findingposition
