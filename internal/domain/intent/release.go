package intent

import "path"

// This file (release.go) implements Step 50's own ("release PR review",
// §15.1) release-PR detection: "a PR is treated as a release review when
// it matches a configurable pattern: originates from/targets a
// release/* branch, or carries a release label." Pure per §11.
//
// Unlike review-vs-request/plan-vs-build (Step 36), release-vs-feature
// has an EXHAUSTIVE, deterministic answer for every PR: a branch name
// and label set are always available, with no free-text ambiguity case
// anywhere in the spec the way an @mention's own prose can be ambiguous.
// DetectRelease below is therefore the actual, authoritative gate a
// caller uses to decide whether to run the manifest check (§15.2) --
// TargetRelease/TargetFeature (rubric.go) exist as this category's own
// vocabulary within the shared intent-classification seam (§18.6: "the
// classifier serves multiple independent categories through the SAME
// contract, rubric, and record shape... release-vs-feature... one more
// category"), should a future classifier-based corroboration pass ever
// be wired for it (mirroring §18.2's CorroborateTarget pattern), but this
// Step does not add a speculative LLM call with no described free-text
// signal to classify -- see this Step's own PR description for the full
// reasoning.

// MatchesReleaseBranch reports whether branch matches pattern, a simple
// shell-style glob (§15.1's own literal "release/*" wording). Uses the
// standard library's path.Match -- pure, no I/O despite the "path"
// package name: it operates purely on the two input strings, never
// touching a filesystem. An empty branch/pattern, or a malformed pattern
// (path.Match's own ErrBadPattern), never matches -- fails conservative
// to "not a release branch" rather than panicking or silently matching
// everything.
func MatchesReleaseBranch(branch, pattern string) bool {
	if branch == "" || pattern == "" {
		return false
	}
	ok, err := path.Match(pattern, branch)
	if err != nil {
		return false
	}
	return ok
}

// HasReleaseLabel reports whether labels contains releaseLabel, an EXACT
// (case-sensitive) match -- mirrors internal/adapters/inbound/github's
// own parsePullRequestLabeled (payload.go): "a plain, exact string
// comparison", the same deterministic, no-model-in-the-loop discipline
// that package's own manual re-trigger label already uses. An empty
// releaseLabel never matches any real label (mirrors
// parsePullRequestLabeled's own "empty configured label... this lane
// never fires" precedent), never a wildcard.
func HasReleaseLabel(labels []string, releaseLabel string) bool {
	if releaseLabel == "" {
		return false
	}
	for _, l := range labels {
		if l == releaseLabel {
			return true
		}
	}
	return false
}

// DetectRelease is §15.1's own deterministic release-PR detection rule:
// "originates from/targets a release/* branch, or carries a release
// label" -- an OR across three checks (head branch, base branch, label).
// headBranch/baseBranch may legitimately be empty when unresolved (never
// matches, MatchesReleaseBranch's own doc comment); labels is the PR's
// current label set; branchPattern/releaseLabel are this deployment's own
// configured values (platform.Config.GitHubReleaseBranchPattern/
// GitHubReleaseLabel) -- either left empty simply never matches on that
// axis (this function's own two helpers' identical "empty means this
// check never fires" discipline), never a wildcard default.
func DetectRelease(headBranch, baseBranch string, labels []string, branchPattern, releaseLabel string) bool {
	if MatchesReleaseBranch(headBranch, branchPattern) {
		return true
	}
	if MatchesReleaseBranch(baseBranch, branchPattern) {
		return true
	}
	return HasReleaseLabel(labels, releaseLabel)
}
