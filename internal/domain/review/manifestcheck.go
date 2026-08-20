package review

// This file (manifestcheck.go) implements §15's own ("release PR
// review", §15.2) manifest check: extends this package with
// ReleaseManifestCheck-shaped logic, distinct from the per-PR risk-map
// Verdict (verdict.go) above. Every function here is pure per §11 (no
// I/O, no time.Now(), no randomness), and -- matching every OTHER file in
// this package (doc.go's own "zero external imports" precedent, kept
// deliberately here too rather than reaching for the standard library's
// "time"/"strconv" packages, exactly like context.go's own hand-rolled
// itoa/hasTrailingNewline) -- this file imports nothing at all.
//
// # Why MergedPR is its OWN type here, not ports.MergedPR reused directly
//
// internal/app/ports.MergedPR is SourceControl.ListMergedBetween's own
// wire/port-facing return shape (mirroring how CreatePRSpec/PRRef/etc.
// already live directly in that package, never imported from domain).
// /internal/domain has zero external dependencies (CLAUDE.md) and ports
// itself "MUST NOT import internal/adapters/*" the other direction
// (ports/doc.go) -- domain importing app/ports would invert that same
// hexagonal boundary from the opposite side. The caller (a later Step's
// app-layer orchestration) converts ports.MergedPR into this package's
// own MergedPR field-by-field, exactly like internal/adapters/inbound/
// httpapi/reviewverdict.go already converts restdtos.
// PostReviewVerdictRequest into reviewpost.VerdictInput -- a small,
// mechanical, well-precedented boundary conversion, not a shortcut this
// package should try to avoid by reaching across the layering.
//
// # Why HighRiskFlagged is a plain bool, not something this package reads
// # off a label string itself
//
// §15.3's "team's own PR-tiering" is, in this codebase, the review:*-risk
// label vocabulary §8.2's verdict-posting tool already syncs onto a PR
// (internal/domain/reviewpost.LabelHighRisk et al., reviewpost/label.go).
// This package cannot import reviewpost (reviewpost already imports
// review -- the reverse dependency would be a cycle), and per doc.go's
// own established layering, review owns the RiskLevel VALUE vocabulary
// while reviewpost owns how that value becomes a posted label STRING --
// review has never known about label strings, and this Step does not
// change that. The caller (which already imports both review and
// reviewpost to build a Verdict elsewhere) is what checks a constituent
// PR's own current labels against reviewpost.LabelHighRisk and passes the
// resulting bool in here, already computed.

// CIConclusion is a constituent PR's own CI result AT THE COMMIT THAT
// ACTUALLY MERGED (§15.2: "CI conclusion at the merge SHA specifically --
// not the latest SHA, a force-push after approval can hide a run that was
// red when it actually merged"). Exactly three values, the zero value
// deliberately not one of them, matching every other enum in this package
// (doc.go) -- but see ComputeReleaseManifestFindings' own doc comment for
// why CIConclusionUnknown is NOT treated as conservatively as
// CIConclusionFailure here, a deliberate, reasoned departure from this
// package's otherwise-uniform fail-conservative policy.
type CIConclusion string

const (
	// CIConclusionSuccess is CI passing (or reporting no failure) at the
	// merge SHA.
	CIConclusionSuccess CIConclusion = "success"
	// CIConclusionFailure is CI genuinely failing at the merge SHA -- the
	// ONLY value ComputeReleaseManifestFindings ever reports a
	// ManifestFindingRedAtMerge finding for.
	CIConclusionFailure CIConclusion = "failure"
	// CIConclusionUnknown is "no CI signal could be found or determined
	// at this commit" (no checks/statuses configured, or the lookup
	// itself failed) -- NOT evidence of failure. The zero value of this
	// type is treated identically to this value (an unset field is
	// exactly as uninformative as an explicit "unknown").
	CIConclusionUnknown CIConclusion = "unknown"
)

// RevertReviewState is whether a constituent PR's own revert (WasReverted
// == true) itself carried an approving review -- a direct, own-package
// mirror of ports.RevertReviewState (this file's own top doc comment
// explains why MergedPR itself is never the reused ports type directly;
// the same reasoning applies here), mirroring CIConclusion's own
// identical three-value "positively confirmed vs. genuinely unknown"
// shape for the SAME reason: audit-fix (should-fix #4) -- a revert PR
// whose own review state could not be determined must never be treated
// as RevertReviewStateNotReviewed (the ONE value ComputeReleaseManifestFindings
// below ever produces ManifestFindingUnreviewedRevert for), the exact
// same discipline CIConclusionUnknown already establishes just above for
// the red-at-merge finding.
type RevertReviewState string

const (
	// RevertReviewStateReviewed is a CONFIRMED approving review on the
	// revert PR itself.
	RevertReviewStateReviewed RevertReviewState = "reviewed"
	// RevertReviewStateNotReviewed is a CONFIRMED absence of any
	// approving review on the revert PR itself -- the only value that
	// ever produces ManifestFindingUnreviewedRevert.
	RevertReviewStateNotReviewed RevertReviewState = "not_reviewed"
	// RevertReviewStateUnknown is "the revert PR's own review state could
	// not be determined" -- NOT evidence of an unreviewed revert. The
	// zero value of this type is treated identically to this value,
	// mirroring CIConclusion's own identical convention.
	RevertReviewStateUnknown RevertReviewState = "unknown"
)

// MergedPR is one constituent PR the manifest check (§15.2) and the
// aggregate-review decision function (§15.3, aggregatereview.go)
// examine. See this file's own top doc comment for why this is a
// separate type from ports.MergedPR, never a reused/aliased one.
type MergedPR struct {
	// Number and Title identify the PR for a rendered finding/comment.
	Number int
	Title  string

	// HasApprovingReview is whether this PR carried at least one review
	// with state APPROVED at merge time.
	HasApprovingReview bool
	// MergedViaAdminOverride is whether this PR merged via an admin/
	// policy bypass of a review requirement it did not satisfy --
	// meaningful only alongside HasApprovingReview == false; see this
	// file's own top doc comment and the port adapter's for how a
	// caller actually determines this (a positive, confirmed fact, never
	// guessed at when undeterminable).
	MergedViaAdminOverride bool

	// CIConclusionAtMergeSHA is this PR's CI result at the exact commit
	// that landed -- see CIConclusion's own doc comment.
	CIConclusionAtMergeSHA CIConclusion

	// WasReverted is whether this PR was later reverted. RevertReviewState
	// is whether THAT revert itself carried an approving review --
	// meaningless when WasReverted is false. See RevertReviewState's own
	// doc comment for why this is a tri-state, not a plain bool
	// (audit-fix should-fix #4).
	WasReverted       bool
	RevertReviewState RevertReviewState
	// RevertedAfterMergeSeconds is how long after this PR's own merge the
	// revert landed -- nil when WasReverted is false, or the timing
	// genuinely could not be determined. A plain integer (seconds), never
	// a time.Time/time.Duration -- see this file's own top doc comment
	// for why this package imports nothing at all, including "time".
	RevertedAfterMergeSeconds *int64

	// HadManualConflictResolution is whether landing this PR required
	// manually resolving a merge conflict against its base -- one of
	// §15.3's own three OR-conditions for the aggregate review.
	HadManualConflictResolution bool
	// ChangedPathPrefixes is the set of top-level path prefixes this
	// PR's diff touches (e.g. "internal/domain/review") -- deduplicated
	// by the caller is NOT required (hasOverlappingPathPrefixes,
	// aggregatereview.go, deduplicates per-PR itself), used to detect
	// "≥3 constituent PRs touch overlapping path prefixes (same
	// subsystem)" (§15.3).
	ChangedPathPrefixes []string
	// HighRiskFlagged is §15.3's own "flagged high-risk/critical by the
	// team's own PR-tiering" signal -- see this file's own top doc
	// comment for why this package receives it as an already-computed
	// bool rather than deriving it from a label string itself.
	HighRiskFlagged bool
}

// ManifestFindingKind is the release manifest check's own closed,
// mechanical finding vocabulary (§15.2's own three example findings).
// Deliberately NOT reviewpost.Finding/reviewpost.SentinelKind: a manifest
// finding is an audit fact about a PAST merge, never a risk-map finding
// about the PR currently under review, and never sentinel-auto-fix-
// eligible (§17.1's own SentinelKind vocabulary is unrelated and
// unaffected) -- see doc.go's design call #4 for why no general Finding
// type ships in this package at all; this is a narrower, Step-50-specific
// shape, not an attempt to satisfy that broader gap.
type ManifestFindingKind string

const (
	// ManifestFindingUnreviewedMerge is a constituent PR merged without
	// an approving review (§15.2's own first example: "PR #142 merged
	// without an approving review (admin override)").
	ManifestFindingUnreviewedMerge ManifestFindingKind = "unreviewed_merge"
	// ManifestFindingRedAtMerge is a constituent PR's CI CONFIRMED
	// failing at the commit that actually merged (§15.2's own second
	// example: "PR #156 was red at its merge SHA").
	ManifestFindingRedAtMerge ManifestFindingKind = "red_at_merge"
	// ManifestFindingUnreviewedRevert is a constituent PR later
	// reverted, where that revert itself carried no approving review
	// (§15.2's own third example: "PR #160 was reverted 2h after merge;
	// the revert itself was unreviewed").
	ManifestFindingUnreviewedRevert ManifestFindingKind = "unreviewed_revert"
)

// ManifestFinding is one mechanical, compliance-style finding the release
// manifest check produces for one constituent PR -- "an audit, not a risk
// verdict" (§15.2's own words): no RiskLevel, no Shippable, nothing that
// would make this look like an ordinary per-PR risk-map verdict finding
// (§15.4: this check "stay[s] exactly the mechanical/compositional pass[]
// specified... with no release-level premise or shippable score").
type ManifestFinding struct {
	Kind     ManifestFindingKind
	PRNumber int
	PRTitle  string
	// Detail is a short, optional elaboration specific to Kind -- e.g.
	// "admin override" for ManifestFindingUnreviewedMerge when
	// MergedViaAdminOverride was CONFIRMED, or a revert-timing phrase
	// (e.g. "2h") for ManifestFindingUnreviewedRevert. Empty is
	// legitimate: not every finding of a given Kind has extra detail to
	// add.
	Detail string
}

// ComputeReleaseManifestFindings is the release manifest check's (§15.2)
// single exported pure function: given the PRs merged into a release PR's
// own head since it diverged from its base (a caller-converted
// []MergedPR -- see this file's own top doc comment), returns every
// mechanical, compliance-style finding worth surfacing. "Fully
// mechanizable: no code-reasoning required, this is a compliance check,
// not a code review" (§15.2) -- every finding below is a direct,
// deterministic read of already-known facts, nothing an LLM is ever asked
// to judge. Order is stable: findings are appended in the same order
// merged is given (the caller's own, typically chronological order),
// never re-sorted; a single PR may contribute zero, one, two, or all
// three finding kinds.
//
// CIConclusionUnknown is DELIBERATELY NOT treated as a red-at-merge
// finding, a reasoned departure from doc.go's otherwise-uniform "an
// unrecognized/unassessed value fails as conservatively as the worst
// known one" policy elsewhere in this package. Shippable's own fail-
// conservative computation only ever routes a PR toward stricter human
// review -- it asserts nothing false. A manifest finding is a direct,
// human-facing factual claim ("PR #156 WAS red at its merge SHA") posted
// verbatim to a maintainer; asserting that claim for a PR whose CI status
// simply could not be determined (no checks configured, or the lookup
// itself failed) would be dishonest, not merely cautious -- the harm of a
// false accusation outweighs the harm of a silent miss here, unlike
// everywhere else in this package where "erring toward a human looking
// closer" carries no such cost. Only a POSITIVELY CONFIRMED
// CIConclusionFailure ever produces this finding.
func ComputeReleaseManifestFindings(merged []MergedPR) []ManifestFinding {
	var findings []ManifestFinding
	for _, pr := range merged {
		if !pr.HasApprovingReview {
			detail := ""
			if pr.MergedViaAdminOverride {
				detail = "admin override"
			}
			findings = append(findings, ManifestFinding{
				Kind:     ManifestFindingUnreviewedMerge,
				PRNumber: pr.Number,
				PRTitle:  pr.Title,
				Detail:   detail,
			})
		}

		if pr.CIConclusionAtMergeSHA == CIConclusionFailure {
			findings = append(findings, ManifestFinding{
				Kind:     ManifestFindingRedAtMerge,
				PRNumber: pr.Number,
				PRTitle:  pr.Title,
			})
		}

		// Audit-fix should-fix #4: ONLY a positively confirmed
		// RevertReviewStateNotReviewed ever produces this finding --
		// RevertReviewStateUnknown (a failed sub-fetch) and the zero
		// value both fall through here exactly like CIConclusionUnknown
		// already falls through the red-at-merge check above, for the
		// identical reason (this package's own doc comment on that
		// check: never manufacture a human-facing accusation from a
		// fetch failure).
		if pr.WasReverted && pr.RevertReviewState == RevertReviewStateNotReviewed {
			detail := ""
			if pr.RevertedAfterMergeSeconds != nil {
				detail = formatApproxDuration(*pr.RevertedAfterMergeSeconds)
			}
			findings = append(findings, ManifestFinding{
				Kind:     ManifestFindingUnreviewedRevert,
				PRNumber: pr.Number,
				PRTitle:  pr.Title,
				Detail:   detail,
			})
		}
	}
	return findings
}

// formatApproxDuration renders seconds as a short, approximate duration
// string (e.g. "2h", "45m", "30s") -- a hand-rolled stand-in for
// time.Duration.String(), kept inline so this file needs no import at
// all (see this file's own top doc comment for why). A negative input
// (never expected from a real caller -- a revert cannot land before its
// own PR merged) is treated as 0 rather than producing a nonsensical
// negative duration string.
func formatApproxDuration(seconds int64) string {
	if seconds < 0 {
		seconds = 0
	}
	if hours := seconds / 3600; hours >= 1 {
		return itoa64(hours) + "h"
	}
	if minutes := seconds / 60; minutes >= 1 {
		return itoa64(minutes) + "m"
	}
	return itoa64(seconds) + "s"
}

// itoa64 is a tiny, dependency-free int64->string helper -- a stand-in
// for strconv.FormatInt, kept inline so this file needs no import at all
// (mirrors context.go's own identical itoa precedent, the int variant).
func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}
