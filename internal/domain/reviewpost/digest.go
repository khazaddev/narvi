package reviewpost

import "github.com/khazaddev/narvi/internal/domain/review"

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
// parser, on principle" stance. Requested on every review; validation-
// enforced (at least one entry with real, non-blank content -- see
// validate.go's own hasNonBlankArchDecision) ONLY when the posting
// VerdictInput's own ReviewDepth is reviewtriage.DepthDeep (Step 68,
// §26.3, which now exists and defines that deep path) -- see Digest's own
// doc comment below for the full "Enforcement" picture.
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
// UnverifiedLimits}", extended by §26.2/Step 67's own
// "DescriptionAdequacy/AdequacyExplanation/ProposedBody" addition below)
// -- a NEW, ADDITIVE field alongside VerdictInput's
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
// # Enforcement -- Summary/DescriptionAdequacy/AdequacyExplanation always required; ArchDecisions/StackRisks/UnverifiedLimits required too, but only on the deep path; ProposedBody never required
//
// §26.1's own "Enforcement" section, and IMPLEMENTATION_PLAN.md's Step 66
// row verbatim: "Summary required on every review from day one, full
// digest schema-required on the deep path once Step 68 defines it
// (reject-don't-repair at the posting endpoint)". Step 68 (§26.3, the
// light/deep triage) now exists and defines that deep path. Summary
// (ValidateVerdictInput's own ErrEmptyDigestSummary check, validate.go)
// and §26.2/Step 67's own DescriptionAdequacy/AdequacyExplanation
// (ErrInvalidDescriptionAdequacy/ErrEmptyAdequacyExplanation, same file)
// are hard-required on EVERY review, light and deep alike -- the
// description-adequacy check is a normal, always-on part of every review,
// never a deep-path-only requirement, matching how Premise/RiskLevel/
// TestsCoverage were never gated behind a light/deep distinction either.
// ArchDecisions/StackRisks/UnverifiedLimits are REQUESTED on every review
// (review.RenderTurnPrompt's own verdictToolInstructions asks the agent to
// fill them), and additionally REQUIRED -- rejected when empty/blank,
// ValidateVerdictInput's own ErrEmptyDigestArchDecisions/
// ErrEmptyDigestStackRisks/ErrEmptyDigestUnverifiedLimits -- whenever the
// posting VerdictInput's own ReviewDepth is reviewtriage.DepthDeep.
// ProposedBody (§26.2/Step 67) stays REQUESTED-but-never-required on
// every path, deep included -- unlike the other three, §26.3 never made
// it deep-path-mandatory (most reviews, light or deep, propose no PR-body
// rewrite at all), mirroring VerdictInput.Findings' own "additive,
// optional, nil/empty is always legal" precedent (validate.go's own doc
// comment on that field).
type Digest struct {
	// Summary is "What this PR does" -- 2-4 sentences written from the
	// diff, never copied from the PR's own title/body. REQUIRED: see this
	// type's own doc comment above for why, and ValidateVerdictInput's own
	// ErrEmptyDigestSummary for the check.
	Summary string
	// ArchDecisions is zero or more structural decisions the diff makes
	// (§26.1 item 3) -- nil/empty is legal on the LIGHT path (a PR with no
	// structural decision worth naming, e.g. a pure bugfix), but REQUIRED
	// (at least one entry with real, non-blank content) on the DEEP path
	// (Step 68, §26.3) -- see ValidateVerdictInput's own
	// ErrEmptyDigestArchDecisions check and hasNonBlankArchDecision
	// (validate.go) for the exact rule.
	ArchDecisions []ArchDecision
	// StackRisks is free-text prose covering coupling and deployment
	// risks (migrations, multi-phase deploys, image rebuilds) and
	// reversibility (§26.1 item 4) -- alongside the verdict's own
	// pre-existing, typed BlastRadius []review.Tag (review.Verdict,
	// unchanged by this Step), which covers the SAME section's own "blast
	// radius in the existing fixed vocabulary" requirement. Empty string
	// is legal on the LIGHT path, but REQUIRED non-blank on the DEEP path
	// (Step 68, §26.3, ValidateVerdictInput's own ErrEmptyDigestStackRisks)
	// -- rendered only when non-blank (rendercomment.go).
	StackRisks string
	// UnverifiedLimits is the readout's own explicit, honest "what was
	// NOT verified" (§26.1 item 4's own closing clause) -- e.g. "did not
	// run the migration against a production-sized table; did not verify
	// the new retry path under actual network partition". Empty string is
	// legal on the LIGHT path, but REQUIRED non-blank on the DEEP path
	// (Step 68, §26.3, ValidateVerdictInput's own
	// ErrEmptyDigestUnverifiedLimits) -- rendered only when non-blank.
	UnverifiedLimits string

	// DescriptionAdequacy/AdequacyExplanation/ProposedBody (§26.2, Step
	// 67: "review digest: description adequacy + graduated remediation")
	// are this Step's own addition, placed here rather than as new fields
	// on review.Verdict for the SAME reason ArchDecisions/StackRisks/
	// UnverifiedLimits already are (this type's own top doc comment):
	// review's own doc.go pins Verdict at exactly its seven Step-45-named
	// fields, and this content is thematically continuous with the rest
	// of Digest -- the adequacy check's own reference text IS Digest.
	// Summary (§26.2's own words: "compares its own diff-derived Digest.
	// Summary... against title+body"), and a description rewrite proposal
	// is exactly the kind of "additive, requested" narrative content this
	// struct already carries for ArchDecisions/StackRisks/
	// UnverifiedLimits.
	//
	// DescriptionAdequacy is the typed tri-state itself
	// (review.DescriptionAdequacy, NOT a reviewpost-local type) -- it must
	// be review's own type because it is ALSO the direct input to
	// review.AdequacyFloor/review.ComputeShippable (the third raise-only
	// floor, internal/domain/review/adequacy.go): unlike ArchDecisions/
	// StackRisks/UnverifiedLimits, which are pure narrative with no
	// bearing on Shippable, DescriptionAdequacy is validated against
	// review's own closed three-value enum (ValidateVerdictInput's own
	// ErrInvalidDescriptionAdequacy, validate.go) and REQUIRED on every
	// verdict from this Step on, the SAME "closed-enum, hard-validated"
	// treatment RiskLevel/Premise/TestsCoverage already get -- not
	// Summary's own weaker "just check non-blank" treatment.
	DescriptionAdequacy review.DescriptionAdequacy
	// AdequacyExplanation is the tri-state's own required one-line
	// explanation (§26.2's own words: "plus a one-line explanation") --
	// free text, REQUIRED non-blank (ValidateVerdictInput's own
	// ErrEmptyAdequacyExplanation), mirroring Summary/Digest.Summary's own
	// identical "a verdict with no human-readable explanation at all
	// defeats the point" treatment.
	AdequacyExplanation string
	// ProposedBody is the agent's OWN optional rewrite proposal for the
	// PR's body -- §26.2's own "the agent MAY rewrite the PR body"
	// wording: empty is legal (not every review proposes a rewrite, and
	// this field is not validation-enforced in this Step, mirroring
	// ArchDecisions/StackRisks/UnverifiedLimits' own identical "requested,
	// not required" precedent above). When non-blank, RenderVerdictComment
	// (rendercomment.go) renders it in the digest as a suggestion, for
	// EVERY PR regardless of authorship -- graduated remediation (§26.2)
	// is about which PRs additionally get a REAL WRITE (Narvi-authored,
	// per-repo descriptionAutofix flag on, delivered via the outbox and
	// re-verified server-side at delivery time, internal/app/outboxworker's
	// own description-autofix notifier), never about which PRs see this
	// rendered suggestion at all. The title is never proposed or rewritten
	// here or anywhere else (§26.2: "the title is never rewritten
	// automatically, in either case") -- this field carries body content
	// only.
	ProposedBody string

	// ContestedPoints (§26.4, Step 69) is the deep path's own "Contested
	// points" digest section -- free-text prose naming where the primary
	// reviewer's own findings/digest and the counter-reviewer sub-task's
	// own adjudication genuinely disagreed (§26.4: "inter-agent
	// disagreement is precisely the signal that a human must decide").
	// Requested, never required, on EVERY path, deep included -- mirrors
	// ProposedBody's own identical "requested but optional" treatment
	// immediately above, for the identical reason: most deep reviews
	// produce NO disagreement at all (the ordinary, common case is the
	// counter-reviewer confirming the primary's own findings outright),
	// and this section exists to be rendered only when there genuinely is
	// something contested, never padded to satisfy a requiredness check.
	// Empty string is legal on every path (light path most of all, since
	// there is no counter-reviewer there to disagree with anything at
	// all, §26.9) -- rendered only when non-blank, as its own "Contested
	// points" section (rendercomment.go), mirroring renderProposedBody's
	// own "no section header at all when blank" treatment of ProposedBody
	// immediately above, for the identical reason: most reviews (every
	// light-path review, and most deep-path ones too) have nothing
	// contested to report.
	ContestedPoints string
}
