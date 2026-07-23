package github

import (
	"encoding/json"
	"fmt"
	"regexp"
)

// eventTypeIssueComment and eventTypePullRequestReviewComment are the only
// two "X-GitHub-Event" values that can carry a PR @mention (doc.go's own
// writeup) -- GitHub's real event names, verbatim.
const (
	eventTypeIssueComment             = "issue_comment"
	eventTypePullRequestReviewComment = "pull_request_review_comment"
	commentActionCreated              = "created"
)

// mention is the detected result of a single webhook delivery: a real,
// action="created" comment on a pull request that mentions the
// configured bot handle. Zero value (ok=false from parseMention) means
// "nothing to act on" -- not an error.
type mention struct {
	// RepoFullName is the CLAIM KEY repo -- always the base/upstream repo
	// the webhook itself is registered on (GitHub's own top-level
	// "repository" field, guaranteed present and stable across event
	// types), never the head/fork repo. Two mentions on the SAME PR must
	// resolve to the SAME RepoFullName regardless of which fork the head
	// branch lives in, since this is exactly what github_pr_sessions'
	// (repo_full_name, pr_number) coalescing key is keyed on.
	RepoFullName string
	// RepoName/RepoCloneURL are the repo to actually CLONE for the
	// session -- the PR's own HEAD repo (may be a fork), when the event
	// type carries it directly (pull_request_review_comment); falls back
	// to the base repo for issue_comment, which does not (see
	// parseIssueComment's own doc comment for why).
	RepoName     string
	RepoCloneURL string
	PRNumber     int32
	// HeadBranch is nil when the event type doesn't carry the PR's head
	// ref directly (issue_comment -- see parseIssueComment) -- nil means
	// "use the repo's own default branch" per restdtos.
	// CreateSessionRequestReposElemBranch's own documented convention,
	// exactly like every other ingress caller of that same field.
	HeadBranch  *string
	CommentBody string
}

// parseMention dispatches on eventType and reports whether body is a
// genuine, actionable PR @mention. ok=false (nil error) means "ignore
// this delivery" -- an unrecognized event type, a non-"created" comment
// action, a plain-issue (not PR) comment, or a comment that doesn't
// mention botHandle. A non-nil error means the body itself failed to
// parse as the shape its own X-GitHub-Event header claims.
func parseMention(eventType string, body []byte, mentionRE *regexp.Regexp) (mention, bool, error) {
	switch eventType {
	case eventTypeIssueComment:
		return parseIssueComment(body, mentionRE)
	case eventTypePullRequestReviewComment:
		return parsePullRequestReviewComment(body, mentionRE)
	default:
		return mention{}, false, nil
	}
}

// issueCommentPayload is the subset of GitHub's real "issue_comment"
// webhook payload this adapter needs (verified against GitHub's own live
// webhook-events documentation during this Step's own design phase).
// issue_comment fires for comments on BOTH plain issues and pull requests
// -- GitHub's own REST/webhook model treats a PR as a kind of issue --
// distinguished only by whether Issue.PullRequest is present at all.
//
// KNOWN LIMITATION: unlike pull_request_review_comment below, this
// payload does NOT embed the PR's own head repo/branch anywhere -- only a
// `pull_request.url` link a caller would need a further, authenticated
// GitHub REST API call to resolve. No such call is wired here: Step 32 is
// explicitly the ingress layer only (doc.go), and no GitHub App/bot
// credential plumbing for an outbound authenticated call exists yet in
// this codebase. A mention delivered via issue_comment therefore falls
// back to the BASE repo's own default branch (HeadBranch left nil) rather
// than the PR's actual head branch -- correct for a same-repo PR, wrong
// for a fork PR. This is a deliberate, honestly-documented simplification
// for this Step; resolving it precisely is left to whichever later Step
// wires a real outbound GitHub API credential for this purpose.
type issueCommentPayload struct {
	Action string `json:"action"`
	Issue  struct {
		Number      int32 `json:"number"`
		PullRequest *struct {
			URL string `json:"url"`
		} `json:"pull_request"`
	} `json:"issue"`
	Comment struct {
		Body string `json:"body"`
	} `json:"comment"`
	Repository struct {
		FullName string `json:"full_name"`
		Name     string `json:"name"`
		CloneURL string `json:"clone_url"`
	} `json:"repository"`
}

func parseIssueComment(body []byte, mentionRE *regexp.Regexp) (mention, bool, error) {
	var p issueCommentPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return mention{}, false, fmt.Errorf("github: unmarshal issue_comment payload: %w", err)
	}
	if p.Action != commentActionCreated {
		return mention{}, false, nil
	}
	if p.Issue.PullRequest == nil {
		// A comment on a plain issue, not a PR -- §8.2 is PR review only.
		return mention{}, false, nil
	}
	if !mentionRE.MatchString(p.Comment.Body) {
		return mention{}, false, nil
	}
	return mention{
		RepoFullName: p.Repository.FullName,
		RepoName:     p.Repository.Name,
		RepoCloneURL: p.Repository.CloneURL,
		PRNumber:     p.Issue.Number,
		HeadBranch:   nil, // see issueCommentPayload's own KNOWN LIMITATION doc comment.
		CommentBody:  p.Comment.Body,
	}, true, nil
}

// pullRequestReviewCommentPayload is the subset of GitHub's real
// "pull_request_review_comment" webhook payload this adapter needs
// (verified against GitHub's own live webhook-events documentation).
// Unlike issue_comment, this event's payload embeds the FULL pull_request
// object, including its head repo/branch directly -- no further API call
// needed to derive them.
type pullRequestReviewCommentPayload struct {
	Action  string `json:"action"`
	Comment struct {
		Body string `json:"body"`
	} `json:"comment"`
	PullRequest struct {
		Number int32 `json:"number"`
		Head   struct {
			Ref  string `json:"ref"`
			Repo struct {
				Name     string `json:"name"`
				CloneURL string `json:"clone_url"`
			} `json:"repo"`
		} `json:"head"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func parsePullRequestReviewComment(body []byte, mentionRE *regexp.Regexp) (mention, bool, error) {
	var p pullRequestReviewCommentPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return mention{}, false, fmt.Errorf("github: unmarshal pull_request_review_comment payload: %w", err)
	}
	if p.Action != commentActionCreated {
		return mention{}, false, nil
	}
	if !mentionRE.MatchString(p.Comment.Body) {
		return mention{}, false, nil
	}
	headBranch := p.PullRequest.Head.Ref
	return mention{
		RepoFullName: p.Repository.FullName,        // base/upstream repo -- the claim key (see mention.RepoFullName's own doc comment).
		RepoName:     p.PullRequest.Head.Repo.Name, // head repo -- may be a fork; the repo to actually clone.
		RepoCloneURL: p.PullRequest.Head.Repo.CloneURL,
		PRNumber:     p.PullRequest.Number,
		HeadBranch:   &headBranch,
		CommentBody:  p.Comment.Body,
	}, true, nil
}

// compileMentionPattern builds the case-insensitive "@handle" detector
// mentionRE (used by parseIssueComment/parsePullRequestReviewComment
// above) for a configured bot handle. Deliberately a simple heuristic, not
// an attempt at fully robust NLP mention-parsing:
//
//   - A trailing negative class, `($|[^a-zA-Z0-9_./-])`, requires whatever
//     immediately follows the handle to NOT itself be a character a real
//     GitHub username can be extended with (alnum, "_", or "-"), NOR a
//     "/" or "." -- rejecting both a comment that mentions some OTHER,
//     longer handle merely starting with this one (e.g. handle "narvi"
//     must not match "@narvi-bot-2"; Go RE2's plain `\b` word-boundary
//     alone is NOT sufficient here, since "-" itself already counts as a
//     word boundary, letting "narvi-bot-2" slip through a bare `\b`
//     check) AND a GitHub team mention sharing the handle as a prefix
//     (e.g. handle "narvi" must not match "@narvi/maintainers" -- "/"
//     always starts a team's slug half in "@org/team", and "." can
//     appear in one directly after an org name too).
//   - A negative-lookbehind-equivalent leading class,
//     `(^|[^a-zA-Z0-9_./-])`, requires whatever immediately precedes the
//     "@" to NOT itself be an identifier character -- rejecting an email
//     address's local part directly touching "@" (e.g. "user@narvi...")
//     from being misread as a mention, since Go's RE2 engine has no real
//     lookbehind.
//
// regexp.QuoteMeta escapes botHandle so any regex metacharacter it
// happens to contain (unlikely for a real GitHub handle, but not
// validated elsewhere) is matched literally, never reinterpreted.
func compileMentionPattern(botHandle string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(^|[^a-zA-Z0-9_./-])@` + regexp.QuoteMeta(botHandle) + `($|[^a-zA-Z0-9_./-])`)
}
