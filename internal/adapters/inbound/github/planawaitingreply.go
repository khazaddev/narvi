// This file (planawaitingreply.go) closes Finding 1 of this batch's own
// follow-up fix (§8.1): GitHub's own bot-ingress path
// (httpapi.CreateTurnForBot, bot.go, reused by coalesce.go's REUSE path)
// already hits the SAME awaiting-plan gate (createTurnLocked, httpapi/
// turn.go) that Slack/Linear ingress hit -- but until this fix, the
// sentinel (httpapi.ErrPlanAwaitingApproval) never survived
// CreateTurnForBot's own re-wrap (a "%s" verb, not "%w", discarded the
// error chain entirely), so handler.go's error switch could never
// recognize it via errors.Is, fell into the generic-error branch,
// released the webhook delivery claim for a pointless GitHub redelivery
// retry (the awaiting-plan condition persists until a human decides
// elsewhere -- a retry can never help), and the PR thread got no
// clarifying reply at all, unlike Slack/Linear.
//
// This is now fixed in two parts: bot.go's own CreateTurnForBot wraps via
// "%w" (preserving the chain), and handler.go's error switch recognizes
// errors.Is(err, httpapi.ErrPlanAwaitingApproval) as a deterministic,
// expected business state -- not a transient failure -- acknowledging 200
// WITHOUT releasing the delivery claim (mirrors ErrActorNotAuthorized's
// own identical "no release, retrying changes nothing" precedent,
// coalesce.go) and posting the honest reply this file defines below.

package github

import (
	"context"
	"log/slog"

	"github.com/khazaddev/narvi/internal/domain/reposource"
)

// CommentPoster is the narrow slice of githubapi.Adapter's own real,
// authenticated GitHub REST API surface (PostIssueComment) this package
// needs to post an honest, non-error reply on a PR thread -- mirrors
// PullRequestResolver's own identical narrow-interface precedent
// (headresolve.go): a small, locally-defined interface (not one of
// internal/app/ports' general-purpose abstractions -- posting a reply to
// THIS package's own mention-shaped ingress is not a general "notifier"
// operation another adapter would ever implement independently) so tests
// can inject a fake with no real HTTP round trip. githubapi.Adapter
// satisfies this exactly (the SAME instance production wiring already
// constructs for CreatePR/ResolveBranchSHA/ResolveContractsFingerprint/
// GetPullRequest, cmd/control-plane/main.go -- never a second,
// independently-constructed copy), with no adapter-side change needed.
type CommentPoster interface {
	PostIssueComment(ctx context.Context, owner, repo string, prNumber int, token, body string) error
}

// planAwaitingApprovalReplyText is this batch's own honest reply, posted
// back to the PR thread when coalesce.go's CreateOrJoin declines to
// enqueue a build turn because the session's plan is currently
// StatusAwaitingApproval -- mirrors Slack's ackPlanAwaitingText (handler.go)
// / Linear's planAwaitingApprovalReplyText (webhook.go) tone exactly.
// Deliberately does NOT reference plandomain.RevisePrefix the way those
// two do: unlike Slack/Linear, GitHub's own mention ingress (coalesce.go)
// never parses plandomain.MatchVerdict/MatchRevise out of a comment body
// at all (a GitHub mention always dispatches with req.PlanMode's zero
// value, false) -- claiming a revise: prefix would work here would be
// dishonest. Instead this points the user at the channels that actually
// CAN decide a plan today (Slack/Linear's own text/button verdicts, or the
// REST approve/reject endpoints behind the web dashboard).
const planAwaitingApprovalReplyText = "A plan is awaiting approval for this session. Approve, reject, or request changes to it via Slack, Linear, or the web dashboard, then mention this bot again once a decision has been made."

// postPlanAwaitingReply posts planAwaitingApprovalReplyText back to
// repoFullName's prNumber, the same way githubapi.Adapter's own
// outbox-driven turn-outcome notification already posts to a PR
// (PostIssueComment) -- best-effort: poster == nil (no CommentPoster wired
// at all -- e.g. this package's own handler_test.go, which never populates
// Config.Comments) or a failed post are both only logged, never turning
// this otherwise-successful acknowledgment into an error/retry -- the
// underlying awaiting-plan condition is a real, expected business state,
// not a transient failure a GitHub redelivery could fix either way, and a
// failed best-effort reply is no worse than Linear's own postThoughtNotice/
// Slack's own chat.postMessage failure handling (both logged, neither
// fatal to the ack).
func postPlanAwaitingReply(ctx context.Context, logger *slog.Logger, poster CommentPoster, botToken, repoFullName string, prNumber int32) {
	if poster == nil {
		return
	}
	owner, repo, ok := reposource.SplitFullName(repoFullName)
	if !ok {
		logger.Warn("github: could not split repo_full_name into owner/repo, skipping plan-awaiting-approval reply",
			"repo_full_name", repoFullName, "pr_number", prNumber)
		return
	}
	if err := poster.PostIssueComment(ctx, owner, repo, int(prNumber), botToken, planAwaitingApprovalReplyText); err != nil {
		logger.Warn("github: post plan-awaiting-approval reply failed", "error", err, "repo", repoFullName, "pr_number", prNumber)
	}
}
