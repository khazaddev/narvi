package decisioninbox

import (
	"context"

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
// ComputeAutoApprovalEligible) -- never a narrower "just check CI is
// still green" shortcut, since ANY of those facts (a new commit landed
// dropping CI red, a review requested a change, a needs-human label
// applied, the toggle... ) could have changed since the cached queue was
// last rendered.
//
// ok=true only when every check passes, in which case headSHA is the
// PR's own CURRENT head SHA -- the caller passes this straight through to
// MergePR as its own optimistic-concurrency guard (ports.MergePRSpec.
// HeadSHA), so a push landing between this revalidation and the actual
// merge call fails loudly (a 409 from GitHub itself) rather than silently
// merging code nobody just re-checked. ok=false's own reason is a short,
// human-readable explanation suitable for a 409 response body.
func RevalidateForMerge(ctx context.Context, deps Deps, sourceControl ports.SourceControl, actorGitHubID, repoFullName string, prNumber int, token string) (ok bool, headSHA string, reason string, err error) {
	prs, err := sourceControl.ListOpenPRsForUser(ctx, ports.ListOpenPRsForUserSpec{GitHubExternalID: actorGitHubID, Token: token})
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
		return false, "", "this pull request is no longer open, or no longer assigned to you", nil
	}
	if target.Draft {
		return false, "", "this pull request is a draft", nil
	}

	if excluded, exErr := deps.SentinelFixes.ExistsByFixPRNumber(ctx, repoFullName, int32(prNumber)); exErr == nil && excluded {
		return false, "", "this pull request is a sentinel-auto-fix follow-up -- it merges automatically once its own checks pass, never through this endpoint", nil
	}

	hasNeedsHuman, riskLabel, isHandoffPR := classifyPRLabels(target.Labels)
	if isHandoffPR {
		return false, "", "this pull request is a handoff item, not an ordinary code-review merge decision", nil
	}

	if !isPlatformAuthored(ctx, deps, target.HTMLURL) {
		return false, "", "this pull request was not authored by a platform session", nil
	}

	openFindings := countOpenFindings(ctx, deps, repoFullName, prNumber)
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
