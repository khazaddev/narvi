package decisioninbox

import (
	"context"
	"fmt"

	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/domain/decisioninbox"
)

// RevalidateForMerge re-checks, LIVE and never cached (§16.2, §5.2's own
// "the rendered queue is never trusted as authority" invariant), whether
// (repoFullName, prNumber) is currently eligible for actorGitHubID to
// merge through the decision inbox's own Merge endpoint. sourceControl is
// taken as a DIRECT parameter here, deliberately never deps.SCMCache --
// the whole point of this function is a fresh read, and threading it
// through the cache would silently defeat that (this is this package's
// ONE caller that must bypass the cache on purpose; every other read in
// this package goes through deps.SCMCache exactly because staleness is
// acceptable there, per §16.2's own "SCM data is cached... never
// presented as live truth" -- an action endpoint is the one place "live
// truth" is exactly what's required).
//
// Re-derives the EXACT SAME criteria buildPROpenItem used to classify
// this PR as ready_to_merge in the first place (§17 exclusion, not a
// draft, not a handoff PR, platform-authored, and this Step's own interim
// ComputeAutoApprovalEligible), PLUS one fact the read model fetches but
// never gates on (HasChangesRequested, immediately below) -- never a
// narrower "just check CI is still green" shortcut, since ANY of these
// facts (a new commit landed dropping CI red, a reviewer requested
// changes, a needs-human label applied, the toggle...) could have
// changed since the cached queue was last rendered.
//
// The "approval state" this function re-checks (§60 review finding A4) is
// SPECIFICALLY GitHub's own HasChangesRequested -- treated as a hard
// merge blocker, since nobody should be able to one-click merge a PR a
// human explicitly requested changes on, and this fact is already fetched
// for every OpenPR at no extra cost. It deliberately does NOT require
// GitHub's own HasApprovingReview: §16.1 defines ready_to_merge's own
// "approval" as auto-approval BY THE DETERMINISTIC ELIGIBILITY ENGINE
// (ComputeAutoApprovalEligible below), not a human GitHub review --
// requiring a human review here on top of that would silently redefine
// what "auto-approved" means for exactly the population this endpoint
// exists to fast-path. HasApprovingReview is surfaced to the actor as a
// plain display field instead (Item.HasApprovingReview) -- read by
// something, never fetched and then discarded.
//
// A store error from EITHER of the two structural/eligibility sub-checks
// below (the §17 sentinel-fix exclusion, the open-findings count) is
// propagated outright as err, refusing the merge (§60 review findings
// A1/A3's own "fail CLOSED" requirement) -- neither is a legitimate
// "this PR fails eligibility" domain answer the way every OTHER ok=false
// return below is; both are infrastructure failures this function has no
// safe way to interpret as "not blocking", so the caller (httpapi's
// MergePullRequest) surfaces a 500 and the merge simply does not proceed,
// exactly like a failure resolving actorGitHubID's own open-PR list
// itself already does.
//
// ok=true only when every check passes, in which case headSHA is the
// PR's own CURRENT head SHA -- the caller passes this straight through to
// MergePR as its own optimistic-concurrency guard (ports.MergePRSpec.
// HeadSHA), so a push landing between this revalidation and the actual
// merge call fails loudly (a 409 from GitHub itself) rather than silently
// merging code nobody just re-checked. ok=false's own reason is a short,
// human-readable explanation suitable for a 409 response body.
func RevalidateForMerge(ctx context.Context, deps Deps, sourceControl ports.SourceControl, actorGitHubID, repoFullName string, prNumber int, token string) (ok bool, headSHA string, reason string, err error) {
	prs, truncated, err := sourceControl.ListOpenPRsForUser(ctx, ports.ListOpenPRsForUserSpec{GitHubExternalID: actorGitHubID, Token: token})
	if err != nil {
		return false, "", "", err
	}

	var target *ports.OpenPR
	for i := range prs {
		if prs[i].Owner+"/"+prs[i].Repo == repoFullName && prs[i].Number == prNumber {
			target = &prs[i]
			break
		}
	}
	if target == nil {
		if truncated {
			// §60 review finding C1's own truncated signal, applied here:
			// a degraded/partial live read (e.g. one of GitHub's own
			// search queries failed) means this function genuinely cannot
			// tell "not assigned to you" from "we simply failed to see
			// it" -- asserting the former with confidence here would be a
			// false-confident 409 that could discourage a legitimate
			// retry. Fails as an error (500, prompting a retry) instead
			// of a confident domain "no".
			return false, "", "", fmt.Errorf("decisioninbox: revalidate for merge: could not confirm this pull request's current state (a degraded/partial GitHub read) -- please retry")
		}
		return false, "", "this pull request is no longer open, or no longer assigned to you", nil
	}
	if target.Draft {
		return false, "", "this pull request is a draft", nil
	}

	excluded, exErr := deps.SentinelFixes.ExistsByFixPRNumber(ctx, repoFullName, int32(prNumber))
	if exErr != nil {
		return false, "", "", fmt.Errorf("decisioninbox: revalidate for merge: check sentinel-fix exclusion: %w", exErr)
	}
	if excluded {
		return false, "", "this pull request is a sentinel-auto-fix follow-up -- it merges automatically once its own checks pass, never through this endpoint", nil
	}

	hasNeedsHuman, riskLabel, isHandoffPR := classifyPRLabels(target.Labels)
	if isHandoffPR {
		return false, "", "this pull request is a handoff item, not an ordinary code-review merge decision", nil
	}

	if target.HasChangesRequested {
		return false, "", "this pull request has changes requested by a reviewer", nil
	}

	if !isPlatformAuthored(ctx, deps, target.HTMLURL) {
		return false, "", "this pull request was not authored by a platform session", nil
	}

	openFindings, findingsErr := countOpenFindings(ctx, deps, repoFullName, prNumber)
	if findingsErr != nil {
		return false, "", "", fmt.Errorf("decisioninbox: revalidate for merge: count open findings: %w", findingsErr)
	}
	ciGreen := target.CIConclusion == ports.CIConclusionSuccess

	eligible := decisioninbox.ComputeAutoApprovalEligible(decisioninbox.EligibilityInput{
		CIGreen:              ciGreen,
		IsDraft:              target.Draft,
		RiskLabel:            riskLabel,
		HasNeedsHumanLabel:   hasNeedsHuman,
		OpenBlockingFindings: openFindings,
	})
	if !eligible {
		return false, "", "this pull request no longer meets the auto-approval eligibility criteria (CI, risk label, or open findings changed)", nil
	}

	return true, target.HeadSHA, "", nil
}
