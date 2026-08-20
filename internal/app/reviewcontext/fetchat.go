package reviewcontext

import (
	"context"
	"log/slog"

	"github.com/khazaddev/narvi/internal/platform"
)

// This file implements §22.1.1's own "the one diff already in hand" at
// VERDICT-POSTING time, distinct from Fetch's own turn-CREATION-time
// pre-fetch above: httpapi.PostReviewVerdict needs the SAME diff the
// reviewing agent's own turn was anchored to (turns.review_head_sha,
// Step 62 review finding C2) in order to run reviewpost.MatchPosition/the
// internal/app/findingposition relocation fallback against it -- but that
// diff was never persisted anywhere queryable (review.PreFetchedContext.
// Diff lives only in the turn's own rendered prompt text, in memory, at
// dispatch time). FetchDiffAt re-fetches it, pinned to the EXACT
// historical head SHA the turn actually saw, never the PR's current
// (possibly since-moved-on) head -- reusing the SAME Fetcher interface
// Fetch above already establishes, no port change, mirroring §22.1.1's
// own "never a new call path" instruction applied here to diff-fetching
// exactly as it is applied to the LLM port for relocation.

// FetchDiffAt fetches owner/repo#number's own diff against its immediate
// base, pinned to headSHA -- the SAME head SHA a review turn's own prompt
// was originally built against (verdictHeadSHA, resolved by httpapi.
// PostReviewVerdict from turns.review_head_sha), NOT whatever the PR's
// head happens to be right now. Best-effort, exactly like Fetch above: a
// failure at either step (resolving the base ref, or fetching the compare
// diff itself) degrades to ok=false, logged, never an error the caller
// must propagate -- httpapi.PostReviewVerdict's own posting/rendering
// must never fail just because a position-anchoring diff refetch didn't
// go through; every finding simply stays unanchored (§22.1.1's own fail-
// safe posture, internal/app/findingposition.ResolveAll's own empty-diff
// short-circuit).
//
// Only the base ref is re-resolved via a fresh GetPullRequest call --
// headSHA is used AS GIVEN, never re-derived from that same call's own
// (possibly since-moved-on) pr.HeadSHA, which is the whole point of this
// function existing as a DISTINCT entry point from Fetch above rather
// than a caller simply calling Fetch again.
func FetchDiffAt(ctx context.Context, logger *slog.Logger, fetcher Fetcher, timeouts platform.Timeouts, owner, repo string, number int32, token, headSHA string) (diff string, ok bool) {
	if headSHA == "" {
		return "", false
	}

	prCtx, cancel := context.WithTimeout(ctx, timeouts.GitHubGetPRTimeout)
	pr, err := fetcher.GetPullRequest(prCtx, owner, repo, number, token)
	cancel()
	if err != nil {
		logger.Warn("reviewcontext: fetch pull request (for base ref, needed to pin the position-anchoring diff refetch) failed",
			"error", err, "owner", owner, "repo", repo, "pr_number", number)
		return "", false
	}

	diffCtx, cancel := context.WithTimeout(ctx, timeouts.GitHubPRDiffTimeout)
	fetchedDiff, _, err := fetcher.GetCompareDiff(diffCtx, owner, repo, pr.BaseRef, headSHA, token)
	cancel()
	if err != nil {
		logger.Warn("reviewcontext: fetch compare diff for position anchoring failed",
			"error", err, "owner", owner, "repo", repo, "pr_number", number, "head_sha", headSHA)
		return "", false
	}

	return fetchedDiff, true
}
