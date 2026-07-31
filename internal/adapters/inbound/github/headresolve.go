// This file (headresolve.go) closes the H5 audit finding this batch
// (fix/audit-github-pr-payload-correctness) fixes: a PR @mention arriving
// via GitHub's "issue_comment" webhook event (the PR "Conversation" tab,
// the most common way the bot gets mentioned) created the review session
// with HeadBranch left nil, meaning the session cloned the BASE repo's own
// DEFAULT branch -- never the PR's actual head branch -- even for a
// same-repo PR. Only "pull_request_review_comment" (a comment on a
// specific diff line) ever carried the real head branch, since that
// event's own payload embeds it directly (payload.go's own
// pullRequestReviewCommentPayload).
//
// resolveIssueCommentHead below closes that gap with one authenticated
// GitHub REST API call (githubapi.Adapter.GetPullRequest, GET
// /repos/{owner}/{repo}/pulls/{number}), resolving the PR's TRUE head
// branch AND head repo (which may be a fork) BEFORE handler.go turns the
// mention into the session's own repo spec -- mirroring exactly what
// pull_request_review_comment's own payload already carries for free.

package github

import (
	"context"
	"log/slog"
	"strings"

	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
)

// PullRequestResolver is the narrow slice of githubapi.Adapter's own real,
// authenticated GitHub REST API surface (GetPullRequest) this package
// needs -- a small, locally-defined interface (not one of
// internal/app/ports' general-purpose abstractions: this need is specific
// to this one ingress adapter resolving ITS OWN mention shape, not a
// general "source control" operation another adapter would ever implement
// independently, so it does not belong in internal/app/ports) so tests can
// inject a fake with no real HTTP round trip. githubapi.Adapter satisfies
// this exactly, with no adapter-side change needed.
type PullRequestResolver interface {
	GetPullRequest(ctx context.Context, owner, repo string, number int32, token string) (githubapi.PullRequest, error)
}

// resolveIssueCommentHead resolves m's TRUE head branch/repo via
// resolver.GetPullRequest (H5 audit fix) -- called by handler.go ONLY for
// an issue_comment-sourced mention (the one event type whose own payload
// never embeds head.ref/head.repo directly; see issueCommentPayload's own
// doc comment). pull_request_review_comment's own mention already carries
// the real head branch/repo straight out of parsePullRequestReviewComment,
// so handler.go never calls this for that event type at all.
//
// Three distinct outcomes, all logged, none of them fatal to this mention:
//
//   - resolver == nil (no PullRequestResolver wired at all -- e.g. this
//     package's own handler_test.go, which never populates
//     Config.PullRequests): returns m unchanged, no log line (this is
//     expected, deliberate test/minimal wiring, not a failure).
//   - m.RepoFullName does not actually split into "<owner>/<repo>", or the
//     GetPullRequest call itself fails (network error, non-2xx, a
//     cancelled/expired ctx), or GitHub reports an empty head ref: logged
//     as a warning, then returns m EXACTLY as parseIssueComment already
//     produced it -- today's PRE-fix fallback (HeadBranch nil, base
//     repo's own default branch). Mirrors createPRBestEffort's own
//     log-and-continue convention for a failed outbound GitHub API call
//     (internal/app/sessionactor/pushpr.go) rather than dropping the
//     mention/failing the whole webhook delivery over a single
//     best-effort lookup.
//   - A successful call: m.HeadBranch is set to the PR's real head ref.
//     m.RepoName/RepoCloneURL are updated to the PR's real head repo (may
//     be a fork) IF GitHub reported one -- when GitHub's own head.repo was
//     null (the head/fork repo has since been deleted), m.RepoName/
//     RepoCloneURL are left exactly as parseIssueComment already set them
//     (the base repo), mirroring L15's identical fallback for
//     pull_request_review_comment's own sibling nullable field.
func resolveIssueCommentHead(ctx context.Context, logger *slog.Logger, resolver PullRequestResolver, botToken string, m mention) mention {
	if resolver == nil {
		return m
	}

	owner, repo, ok := splitOwnerRepo(m.RepoFullName)
	if !ok {
		logger.Warn("github: could not split repo_full_name into owner/repo, falling back to base repo's own default branch",
			"repo_full_name", m.RepoFullName, "pr_number", m.PRNumber)
		return m
	}

	pr, err := resolver.GetPullRequest(ctx, owner, repo, m.PRNumber, botToken)
	if err != nil {
		logger.Warn("github: resolve pull request head via GitHub API failed, falling back to base repo's own default branch",
			"error", err, "repo", m.RepoFullName, "pr_number", m.PRNumber)
		return m
	}
	if pr.HeadRef == "" {
		logger.Warn("github: GitHub API reported an empty head ref, falling back to base repo's own default branch",
			"repo", m.RepoFullName, "pr_number", m.PRNumber)
		return m
	}

	headBranch := pr.HeadRef
	m.HeadBranch = &headBranch
	if pr.HeadRepoName != "" && pr.HeadRepoCloneURL != "" {
		// The PR's real head repo (may be a fork; identical to what
		// pull_request_review_comment's own payload already carries
		// directly for the sibling event type) -- the repo to actually
		// clone.
		m.RepoName = pr.HeadRepoName
		m.RepoCloneURL = pr.HeadRepoCloneURL
	}
	// else: GitHub's own head.repo was null (the head/fork repo has been
	// deleted) -- keep m.RepoName/RepoCloneURL exactly as parseIssueComment
	// already set them (the base repo), mirroring L15's identical fallback
	// in parsePullRequestReviewComment for the analogous situation.

	return m
}

// splitOwnerRepo splits a GitHub "full_name" (mention.RepoFullName, always
// GitHub's own top-level repository.full_name, "<owner>/<repo>") into its
// owner/repo halves. Deliberately simpler than
// internal/domain/reposource.ParseOwnerRepo (which parses a full git clone
// URL instead -- audit-remediation batch B3 moved that function here from
// what used to be two forks in internal/app/sessionactor and
// internal/app/imagebuild, so this doc comment now names its real, current
// home): GitHub's full_name is already exactly this shape, nothing else to
// strip.
func splitOwnerRepo(fullName string) (owner, repo string, ok bool) {
	parts := strings.SplitN(fullName, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}
