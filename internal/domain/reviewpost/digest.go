package reviewpost

// This file implements Step 66's own restructuring of the rendered verdict
// into a "merge readout" (§26, §26.1: "when agents author most of the code
// under review, the merge DECISION is the bottleneck, not line-by-line
// reading"). Digest/ArchDecision live HERE, in reviewpost -- never as new
// fields on internal/domain/review.Verdict -- for the EXACT reason
// finding.go's own doc comment already establishes for Finding: review's
// own doc.go pins Verdict at "exactly the seven named fields... and
// nothing else" (design call #4), and this package is where Step 47's
// posting-tool payload grows structure the closed review.Verdict type
// itself never will (VerdictInput's own doc comment: "one level up, until
// a later Step gives it a richer, structured home" -- this Step is that
// later Step, for the digest specifically).

// ArchDecision is one structural decision the diff makes -- §26.1 item 3
// ("Architecture choices"), IMPLEMENTATION_PLAN.md's own Step 66 row:
// "decision, implicitly-rejected alternative, convention conformance".
// Every field is the agent's own free-text narrative, exactly like
// VerdictInput.Summary/FindingInput.Description already are -- rendered
// once (rendercomment.go) and never parsed back out of anything posted,
// matching review/doc.go's own "nothing here even imports a markdown
// parser, on principle" stance. Not validation-enforced in this Step:
// §26.1's own "Enforcement" section states the full digest (this type
// included) becomes schema-required only "once §26.3 defines the deep
// path" (Step 68, not yet built) -- see Digest's own doc comment below for
// the one field ahead of that.
type ArchDecision struct {
	// Decision is what the diff actually decided -- e.g. "introduced a new
	// retry queue table rather than reusing the existing outbox".
	Decision string
	// RejectedAlternative is the alternative this decision implicitly
	// passed over -- e.g. "extending the existing outbox's own retry
	// count column". Named explicitly, not left for a reader to infer, so
	// the readout states not just what was built but what was NOT.
	RejectedAlternative string
	// ConventionConformance is this decision's own conformance to the
	// repo's own established conventions (its agent instructions file --
	// CLAUDE.md/AGENTS.md, whichever the repo under review actually uses
	// -- and its existing code patterns) -- e.g. "matches CLAUDE.md's
	// no-I/O-in-domain rule" or "deviates from the repo's own error-
	// wrapping convention in internal/foo". Free text, not a tri-state
	// enum: conformance is a judgment call with room for nuance ("mostly,
	// except for X"), not a closed vocabulary this package would have to
	// arbitrate.
	ConventionConformance string
}

// Digest is the merge readout's own typed content (§26.1, IMPLEMENTATION_
// PLAN.md's Step 66 row: "Digest{Summary, ArchDecisions[], StackRisks,
// UnverifiedLimits}") -- a NEW, ADDITIVE field alongside VerdictInput's
// pre-existing Summary (validate.go), never a replacement for it: the two
// serve different jobs. VerdictInput.Summary is the agent's own free-text
// "why" narrative, rendered as part of the verdict's own UNCHANGED header
// (§26.1 item 1: "risk badge + why-line + shippable class... do not change
// this part") -- it existed before this Step and nothing here alters its
// meaning, its validation, or its position in the rendered comment.
// Digest.Summary is different in kind: "What this PR does", 2-4 sentences
// written FROM THE DIFF, never copied from the PR's own title/body (§26.1
// item 2) -- "simultaneously the human's summary, the reference text for
// the [future §26.2] adequacy check, and the per-PR headline [future §16/
// §21.3 surfaces]". Unlike the pre-existing Summary, Digest's own fields
// are the ones this Step persists to review_verdicts (migrations/
// 000077_review_verdicts_digest.up.sql) -- "digest quality measurable from
// day one" (§26.1) -- which the pre-existing free-text Summary has never
// been (migrations/000067_review_verdicts.up.sql carries no summary
// column at all, and never will; see that table's own doc comment).
//
// # Enforcement -- Summary required now, the rest requested but not yet enforced
//
// §26.1's own "Enforcement" section, and IMPLEMENTATION_PLAN.md's Step 66
// row verbatim: "Summary required on every review from day one, full
// digest schema-required on the deep path once Step 68 defines it
// (reject-don't-repair at the posting endpoint)". Step 68 (§26.3, the
// light/deep triage) does not exist yet -- there is no "deep path" for a
// fuller requirement to attach to today. So THIS Step hard-requires only
// Summary (ValidateVerdictInput's own ErrEmptyDigestSummary check,
// validate.go) -- ArchDecisions/StackRisks/UnverifiedLimits are REQUESTED
// (review.RenderTurnPrompt's own verdictToolInstructions asks the agent to
// fill them) but never rejected when empty/nil, exactly mirroring
// VerdictInput.Findings' own "additive, optional, nil/empty is always
// legal" precedent (validate.go's own doc comment on that field) --
// building a light/deep distinction, or the future hard-requirement, is
// explicitly Step 68's job, not this one's (this Step's own brief: "do not
// attempt to build any light/deep path distinction -- that doesn't exist
// yet either").
type Digest struct {
	// Summary is "What this PR does" -- 2-4 sentences written from the
	// diff, never copied from the PR's own title/body. REQUIRED: see this
	// type's own doc comment above for why, and ValidateVerdictInput's own
	// ErrEmptyDigestSummary for the check.
	Summary string
	// ArchDecisions is zero or more structural decisions the diff makes
	// (§26.1 item 3) -- nil/empty is legal (a PR with no structural
	// decision worth naming, e.g. a pure bugfix), never rejected by
	// ValidateVerdictInput, exactly like VerdictInput.Findings' own
	// "nil/empty is always a legitimate value" precedent.
	ArchDecisions []ArchDecision
	// StackRisks is free-text prose covering coupling and deployment
	// risks (migrations, multi-phase deploys, image rebuilds) and
	// reversibility (§26.1 item 4) -- alongside the verdict's own
	// pre-existing, typed BlastRadius []review.Tag (review.Verdict,
	// unchanged by this Step), which covers the SAME section's own "blast
	// radius in the existing fixed vocabulary" requirement. Empty string
	// is legal (not validation-enforced in this Step, see this type's own
	// doc comment above) -- rendered only when non-blank
	// (rendercomment.go).
	StackRisks string
	// UnverifiedLimits is the readout's own explicit, honest "what was
	// NOT verified" (§26.1 item 4's own closing clause) -- e.g. "did not
	// run the migration against a production-sized table; did not verify
	// the new retry path under actual network partition". Empty string is
	// legal (not validation-enforced in this Step) -- rendered only when
	// non-blank.
	UnverifiedLimits string
}
