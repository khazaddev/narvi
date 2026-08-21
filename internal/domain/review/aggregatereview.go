package review

// This file (aggregatereview.go) implements §15's own ("release PR
// review", §15.3) conditional aggregate-diff-review trigger: a pure
// decision function, "same style as the domain decision functions"
// (§15.3's own words, §3.2/§9.1 -- e.g. sandbox.Transition/turn.
// Transition/gitstate.Transition elsewhere in /internal/domain). Pure
// per §11, and -- matching every other file in this package (doc.go's
// own "zero external imports" precedent) -- this file imports nothing.

// ShouldRunAggregateReview is §15.3's own pure decision function: given
// the SAME []MergedPR the manifest check (manifestcheck.go) was built
// from (this function never re-fetches or re-derives anything of its
// own), reports whether the conditional aggregate diff review should
// fire. Exactly §15.3's three OR-conditions, evaluated in the order that
// section lists them (the order itself carries no meaning -- this is an
// OR, not a priority ladder -- but is kept stable for readability/testing):
//
//  1. ≥3 constituent PRs touch overlapping path prefixes (same
//     subsystem) -- hasOverlappingPathPrefixes below.
//  2. Any constituent PR was flagged high-risk/critical by the team's
//     own PR-tiering -- MergedPR.HighRiskFlagged.
//  3. Any constituent PR's merge required manually resolving a conflict
//     -- MergedPR.HadManualConflictResolution.
func ShouldRunAggregateReview(merged []MergedPR) bool {
	if hasOverlappingPathPrefixes(merged) {
		return true
	}
	for _, pr := range merged {
		if pr.HighRiskFlagged || pr.HadManualConflictResolution {
			return true
		}
	}
	return false
}

// AggregateReviewTriggerReasons reports, in human-readable form, WHICH of
// §15.3's three OR-conditions fired for merged -- added for the release-
// review screen's own trigger banner (§12.2 item 9: "aggregate-diff
// trigger banner showing why the conditional pass fired"), which needs
// more than ShouldRunAggregateReview's own bare bool to explain itself.
// Deliberately generic causes, not the specific PR numbers/path prefix
// involved (unlike the mockup's own illustrative "3 PRs touched
// internal/domain/sandbox/** this release" wording) -- this package holds
// no rendering/formatting concerns (doc.go's own "zero external imports,
// pure functions only" stance), and the SPECIFIC prefix/PR-number detail
// behind an overlapping-path trigger is exactly the kind of thing a
// caller with real formatting/i18n concerns should render, not this
// package. Order matches ShouldRunAggregateReview's own listed order;
// more than one reason may fire at once, and every one that does is
// returned, not just the first (unlike ShouldRunAggregateReview's own
// short-circuiting OR, which only needs to know THAT one fired).
func AggregateReviewTriggerReasons(merged []MergedPR) []string {
	var reasons []string
	if hasOverlappingPathPrefixes(merged) {
		reasons = append(reasons, "3 or more pull requests in this release touched an overlapping subsystem")
	}
	highRisk := false
	manualConflict := false
	for _, pr := range merged {
		if pr.HighRiskFlagged {
			highRisk = true
		}
		if pr.HadManualConflictResolution {
			manualConflict = true
		}
	}
	if highRisk {
		reasons = append(reasons, "a high-risk pull request is included in this release")
	}
	if manualConflict {
		reasons = append(reasons, "a pull request in this release required manually resolving a merge conflict")
	}
	return reasons
}

// hasOverlappingPathPrefixes reports whether at least 3 DISTINCT
// constituent PRs (by Number) touch at least one SHARED path prefix
// (§15.3: "≥3 constituent PRs touch overlapping path prefixes (same
// subsystem)"). A prefix shared by only 1 or 2 PRs does not count -- the
// threshold is explicitly about ≥3 PRs converging on the SAME area, not
// merely "any two PRs happened to touch the same file". Each PR counts
// AT MOST ONCE per prefix: a PR with several changed files under the
// SAME prefix still contributes exactly one to that prefix's own count,
// never one per file.
func hasOverlappingPathPrefixes(merged []MergedPR) bool {
	prefixToPRs := make(map[string]map[int]bool)
	for _, pr := range merged {
		seenForThisPR := make(map[string]bool, len(pr.ChangedPathPrefixes))
		for _, prefix := range pr.ChangedPathPrefixes {
			if prefix == "" || seenForThisPR[prefix] {
				continue
			}
			seenForThisPR[prefix] = true
			if prefixToPRs[prefix] == nil {
				prefixToPRs[prefix] = make(map[int]bool)
			}
			prefixToPRs[prefix][pr.Number] = true
		}
	}
	for _, prs := range prefixToPRs {
		if len(prs) >= 3 {
			return true
		}
	}
	return false
}
