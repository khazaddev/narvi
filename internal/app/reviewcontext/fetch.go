package reviewcontext

import (
	"context"
	"log/slog"

	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/platform"
)

// Fetcher is the narrow slice of *githubapi.Adapter's own real,
// authenticated GitHub REST API surface Fetch needs -- a small, locally-
// defined interface (mirroring internal/adapters/inbound/github's own
// PullRequestResolver precedent, headresolve.go) so a unit test can inject
// a fake with no real HTTP round trip. *githubapi.Adapter satisfies this
// directly, with no adapter-side change beyond what this Step already adds
// to it.
type Fetcher interface {
	GetPullRequest(ctx context.Context, owner, repo string, number int32, token string) (githubapi.PullRequest, error)
	GetPullRequestDiff(ctx context.Context, owner, repo string, number int32, token string) (diff string, truncated bool, err error)
}

// Fetch builds review.PreFetchedContext for owner/repo#number -- the ONE
// shared assembly point every review-session trigger path (a PR @mention,
// a label retrigger, or the manual re-review REST button, §8.2/Step 46)
// calls before building its own review turn's prompt via
// review.RenderTurnPrompt.
//
// Two independent outbound calls, each best-effort (doc.go's own top-level
// section):
//
//  1. GetPullRequestDiff always runs -- the diff itself is what every
//     trigger path needs pre-fetched, regardless of how the PR's stack
//     context (if any) was learned.
//  2. Stack context: knownStack, when non-nil, is used AS-IS with NO
//     second network call -- the label-retrigger webhook path already has
//     GitHub's own stack object inline in its OWN payload (a native
//     pull_request event, §17.6: "only the dedicated pull_request event
//     type is confirmed to [carry stack]"), so re-deriving it via a
//     redundant GetPullRequest call would be a wasted round trip for data
//     the caller already has in hand. knownStack == nil (every OTHER
//     trigger path today: issue_comment/pull_request_review_comment
//     mentions, and the manual REST retrigger button, none of which carry
//     a native pull_request event's own payload) falls back to a fresh
//     GetPullRequest call -- the SAME call issue_comment's own head-branch
//     resolution already makes elsewhere (headresolve.go), extended by
//     this Step to also decode Stack (§17.6's own "incremental addition to
//     a call this ingress already makes" framing) -- but here, unlike that
//     one call site, invoked as a STANDALONE stack-only lookup for every
//     trigger path that doesn't already have the answer for free. This is
//     a deliberately broader reading than §17.6's own narrowest-scoped
//     text (which stops at "extend the ALREADY-existing issue_comment
//     call, don't invent a new one just for stack") -- see this package's
//     own doc comment and this Step's PR description for why: every one
//     of these OTHER trigger paths already pays for a fresh outbound call
//     to fetch the diff (cost item 1 above is mandatory regardless), so
//     paying for one more GetPullRequest call to also learn stack context
//     is a small, bounded addition to a request that is already making a
//     network round trip, not "inventing a new call just for stack" in
//     the sense §17.6 was narrowing against -- and a reviewer's own
//     pre-fetched context is more USEFULLY consistent (stack context
//     present whenever a PR genuinely has one, regardless of which
//     surface triggered this particular review turn) than leaving it
//     present only for the one ingress lane §17.6's own text happened to
//     examine first.
func Fetch(ctx context.Context, logger *slog.Logger, fetcher Fetcher, timeouts platform.Timeouts, owner, repo string, number int32, token string, knownStack *review.StackContext, knownHeadSHA string) review.PreFetchedContext {
	diffCtx, cancel := context.WithTimeout(ctx, timeouts.GitHubPRDiffTimeout)
	diff, truncated, err := fetcher.GetPullRequestDiff(diffCtx, owner, repo, number, token)
	cancel()
	if err != nil {
		logger.Warn("reviewcontext: fetch pull request diff failed, review turn will carry no pre-fetched diff",
			"error", err, "owner", owner, "repo", repo, "pr_number", number)
		diff = ""
		truncated = false
	}

	stack := knownStack
	headSHA := knownHeadSHA
	// A single GetPullRequest call serves BOTH stack (when not already
	// known) and head SHA (§21.1, Step 62; when not already known) --
	// never two independent fallback fetches for two pieces of data this
	// SAME endpoint already returns together. Mirrors this function's own
	// pre-existing "a wasted round trip for data the caller already has
	// in hand" avoidance (this file's own top doc comment) -- extended
	// here to headSHA: the label-retrigger webhook path (the ONE trigger
	// today that supplies knownStack non-nil) also always supplies
	// knownHeadSHA non-empty from that SAME webhook payload, so this
	// fallback fetch is skipped entirely on that path, exactly as before
	// this Step.
	if stack == nil || headSHA == "" {
		prCtx, cancel := context.WithTimeout(ctx, timeouts.GitHubGetPRTimeout)
		pr, err := fetcher.GetPullRequest(prCtx, owner, repo, number, token)
		cancel()
		if err != nil {
			logger.Warn("reviewcontext: fetch pull request (for stack context/head sha) failed, review turn will carry no stack context and this fetch's own review_verdicts row (if any) will have no head sha to record",
				"error", err, "owner", owner, "repo", repo, "pr_number", number)
		} else {
			if stack == nil && pr.Stack != nil {
				stack = &review.StackContext{
					Position:        pr.Stack.Position,
					Size:            pr.Stack.Size,
					UltimateBaseRef: pr.Stack.BaseRef,
					UltimateBaseSHA: pr.Stack.BaseSHA,
				}
			}
			if headSHA == "" {
				headSHA = pr.HeadSHA
			}
		}
		// pr.Stack == nil, err == nil: an ordinary, non-stacked PR -- stack
		// stays nil, exactly like knownStack's own "nothing stack-shaped to
		// add" case.
	}

	return review.PreFetchedContext{Diff: diff, DiffTruncated: truncated, Stack: stack, HeadSHA: headSHA}
}
