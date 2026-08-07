package decisioninbox

import "github.com/khazaddev/narvi/internal/domain/reviewpost"

// EligibilityInput is ComputeAutoApprovalEligible's own input -- every
// field is already-fetched data (never re-fetched by this pure function
// itself, per this package's own no-I/O doc comment).
type EligibilityInput struct {
	// CIGreen is the PR's CI conclusion at its CURRENT head SHA, re-
	// derived live (never historical) -- ports.CIConclusionSuccess.
	CIGreen bool
	// IsDraft excludes a draft PR outright -- a draft is, by GitHub's own
	// definition, not yet asking to be merged at all.
	IsDraft bool
	// RiskLabel is the PR's CURRENT reviewpost.LabelLowRisk/Medium/High
	// label, or "" if the PR has never been risk-labeled at all (e.g. no
	// review has run yet) -- treated identically to a non-low label
	// below: absence of evidence is never evidence of safety.
	RiskLabel string
	// HasNeedsHumanLabel is reviewpost.LabelNeedsHuman's own presence --
	// §21.2's escape hatch, unchanged.
	HasNeedsHumanLabel bool
	// OpenBlockingFindings is the count of this PR's own STILL-OPEN
	// (reviewpost.FindingStatusOpen) review_findings rows -- a rebutted,
	// fix-pending/open/merged, or fix-applied finding does NOT count
	// (each of those already has an explicit resolution, human or
	// automated); only a genuinely untouched open finding blocks.
	OpenBlockingFindings int
}

// ComputeAutoApprovalEligible is Step 60's own INTERIM stand-in for §21.2's
// real, not-yet-built deterministic auto-approval eligibility engine (Step
// 62 -- §21.1's own text: "This table is not yet built (Step 62)",
// confirmed empirically before writing this: neither review_verdicts, nor
// any persisted Shippable/CI-green/diff-size/sensitive-path eligibility
// computation, nor a per-repo auto-merge toggle exists anywhere in this
// codebase as of Step 60 -- grepped directly, zero hits beyond forward-
// references naming Step 62 as their own future owner, e.g. internal/
// domain/reviewpost/formalreview.go's own doc comment).
//
// # Why this function exists at all, and what replaces it
//
// §16.1 defines ready_to_merge as: "auto-approved by the deterministic
// eligibility engine (§21 -- CI green, no floor raised, diff size under a
// configurable threshold, no sensitive path touched...), CI green at
// head, and assigned to the user". Taking that literally would leave
// ready_to_merge PERMANENTLY EMPTY until Step 62 ships its own engine and
// review_verdicts history -- which would make this Step's own read model
// unverifiable against real data and would not match what a reasonable
// reading of "build the read model" means when the thing it reads does
// not durably exist yet. This function instead approximates the SAME
// verdict from what IS durably true today:
//
//   - RiskLabel: the review's own risk-tier label (reviewpost.LabelLowRisk/
//     Medium/High), synced onto the PR by ComputeLabelSync (Step 47,
//     already shipped) every time a verdict posts -- the durable, GitHub-
//     visible trace of what the reviewer's own RiskLevel was AT LAST
//     REVIEW, even though the underlying Shippable/CoverageFloor/
//     PremiseFloor inputs that produced it are never persisted anywhere
//     Step 60 can read back (review_findings, §46, stores per-FINDING
//     mutable status, never the verdict's own aggregate fields).
//   - HasNeedsHumanLabel: reviewpost.LabelNeedsHuman -- ALREADY the one
//     escape hatch §21.2 itself specifies ("a review: needs-human label
//     forces a PR out of auto-approval regardless of criteria"), so this
//     one criterion needs no approximation at all -- it is exactly what
//     §21.2 will keep doing.
//   - OpenBlockingFindings: a nonzero count of STILL-OPEN (never rebutted,
//     never fixed) review_findings for this PR is at least as strong a
//     signal as "a floor was raised" -- an open finding is, definitionally,
//     something nobody has yet said is fine.
//   - CIGreen/IsDraft: re-derived LIVE from the SourceControl port on every
//     call (§16.2's own "CI at head SHA" -- these need no historical
//     persistence at all, unlike the risk assessment above).
//
// This is DELIBERATELY narrower than §21.2's own future criteria list --
// it has no diff-size threshold and no sensitive-path check, because
// neither is configurable-per-repo anywhere in this codebase yet (that
// config surface is repo_settings, and adding auto-merge-eligibility
// columns to it is Step 62's own migration to write, not this Step's to
// pre-empt). The narrower approximation is intentionally CONSERVATIVE in
// the one direction that matters: it can under-populate ready_to_merge
// (miss a PR §21.2 would have auto-approved once diff-size/sensitive-path
// checks exist) but it can never OVER-populate it with something a human
// would call unsafe to one-click merge, since RiskLevelLow + zero open
// findings + no needs-human label + CI green is already a materially high
// bar. See this Step's own PR description for the full write-up of this
// call; Step 62 replaces this function's body (never its callers' own
// shape) the moment review_verdicts and the real engine exist.
//
// Eligible iff the PR is not a draft, CI is green at head, no needs-human
// label is present, there are zero open blocking findings, and the risk
// label is exactly LabelLowRisk.
func ComputeAutoApprovalEligible(in EligibilityInput) bool {
	if in.IsDraft || in.HasNeedsHumanLabel || !in.CIGreen || in.OpenBlockingFindings > 0 {
		return false
	}
	return in.RiskLabel == reviewpost.LabelLowRisk
}
