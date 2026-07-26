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
	// HeadBranch is nil straight out of parseIssueComment (issue_comment's
	// own payload never carries the PR's head ref directly -- see
	// issueCommentPayload's own doc comment) or parsePullRequestReviewComment
	// (which always sets it, that event's payload embeds head.ref
	// directly) -- nil means "use the repo's own default branch" per
	// restdtos.CreateSessionRequestReposElemBranch's own documented
	// convention, exactly like every other ingress caller of that same
	// field. handler.go's own resolveIssueCommentHead (headresolve.go)
	// resolves this to the PR's REAL head branch for an issue_comment
	// mention via a real GitHub API call BEFORE building the session's
	// repo spec (H5 audit fix) -- a nil HeadBranch reaching that repo spec
	// therefore only ever means "that resolution itself failed" (logged,
	// falls back to today's pre-fix behavior), never "issue_comment can't
	// carry this".
	HeadBranch  *string
	CommentBody string

	// CommenterID/CommenterLogin are the GitHub user id/login of the real
	// human who authored this comment ("comment.user.id"/"comment.user.
	// login" -- GitHub's own real webhook shape, IDENTICAL field names/
	// shape across both issue_comment and pull_request_review_comment;
	// verified against GitHub's own live webhook-events documentation
	// during this batch's own design phase). CommenterID is 0 only if the
	// webhook payload itself omitted comment.user entirely (never expected
	// from a real GitHub delivery, but not assumed away).
	//
	// identity.go's own resolveCommenterActor uses CommenterID (never
	// CommenterLogin, which GitHub allows a user to change at any time) to
	// look up a matching Narvi identities row -- see that file's own doc
	// comment for why this needs no auto-linking algorithm, unlike Slack/
	// Linear. CommenterLogin is carried only for logging.
	CommenterID    int64
	CommenterLogin string
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
// Unlike pull_request_review_comment below, this payload does NOT embed
// the PR's own head repo/branch anywhere -- only a `pull_request.url` link.
// parseIssueComment below therefore always leaves HeadBranch nil and
// RepoName/RepoCloneURL set to the base repo; handler.go's own
// resolveIssueCommentHead (headresolve.go) is what actually resolves the
// PR's REAL head branch/repo, via one authenticated GitHub REST API call
// (GET /repos/{owner}/{repo}/pulls/{number}), AFTER parseMention returns
// and BEFORE the mention is turned into the session's own repo spec (H5
// audit fix, batch fix/audit-github-pr-payload-correctness) -- Step 32's
// original version of this file left that resolution as a known,
// honestly-documented limitation (no outbound GitHub API credential
// existed in this codebase yet); internal/adapters/outbound/githubapi's
// existing bot credential (already used for PostIssueComment) closes it
// without needing any new kind of credential.
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
		// User is "who actually wrote this comment" -- GitHub's own real
		// issue_comment webhook shape (comment.user.{id,login}), verified
		// against GitHub's own live webhook-events documentation. Parsed
		// into mention.CommenterID/CommenterLogin below (see that field's
		// own doc comment) -- this batch's own addition, closing the H4
		// audit finding that GitHub ingress never even parsed WHO
		// commented, let alone authorized them.
		User struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
		} `json:"user"`
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
		RepoFullName:   p.Repository.FullName,
		RepoName:       p.Repository.Name,
		RepoCloneURL:   p.Repository.CloneURL,
		PRNumber:       p.Issue.Number,
		HeadBranch:     nil, // resolved later, by handler.go's own resolveIssueCommentHead -- see issueCommentPayload's own doc comment.
		CommentBody:    p.Comment.Body,
		CommenterID:    p.Comment.User.ID,
		CommenterLogin: p.Comment.User.Login,
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
		// User mirrors issueCommentPayload.Comment.User exactly -- GitHub
		// uses the IDENTICAL comment.user.{id,login} shape for both event
		// types (verified against GitHub's own live webhook-events
		// documentation).
		User struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
		} `json:"user"`
	} `json:"comment"`
	PullRequest struct {
		Number int32 `json:"number"`
		Head   struct {
			Ref string `json:"ref"`
			// Repo is a POINTER (L15 audit fix) -- GitHub's own webhook
			// documentation states this field is nullable: null when the
			// head repository has been deleted (e.g. a fork removed after
			// the PR was opened). A plain, non-pointer struct would
			// silently unmarshal a JSON null into an empty-valued struct
			// (empty Name/CloneURL) rather than letting
			// parsePullRequestReviewComment below detect "no head repo"
			// and fall back to the base repo -- mirroring
			// issueCommentPayload.Issue.PullRequest's own identical
			// nullable-pointer convention (that field's own doc comment)
			// for the same "GitHub genuinely omits/nulls this" reason.
			Repo *struct {
				Name     string `json:"name"`
				CloneURL string `json:"clone_url"`
			} `json:"repo"`
		} `json:"head"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
		// Name/CloneURL back parsePullRequestReviewComment's own base-repo
		// fallback below (L15 audit fix) when Head.Repo is nil -- mirrors
		// issueCommentPayload.Repository's own identical Name/CloneURL
		// fields, used for exactly the same "clone the base repo" purpose.
		Name     string `json:"name"`
		CloneURL string `json:"clone_url"`
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
	m := mention{
		RepoFullName:   p.Repository.FullName, // base/upstream repo -- the claim key (see mention.RepoFullName's own doc comment).
		PRNumber:       p.PullRequest.Number,
		HeadBranch:     &headBranch,
		CommentBody:    p.Comment.Body,
		CommenterID:    p.Comment.User.ID,
		CommenterLogin: p.Comment.User.Login,
	}
	if p.PullRequest.Head.Repo != nil {
		m.RepoName = p.PullRequest.Head.Repo.Name // head repo -- may be a fork; the repo to actually clone.
		m.RepoCloneURL = p.PullRequest.Head.Repo.CloneURL
	} else {
		// L15 audit fix: GitHub's own head.repo was null (the head/fork
		// repo has since been deleted) -- fall back to the base repo,
		// exactly like parseIssueComment's own existing fallback for the
		// analogous "no real head repo info available" situation, rather
		// than silently proceeding with an empty RepoName/RepoCloneURL
		// that would make the session try to clone an empty repo spec.
		m.RepoName = p.Repository.Name
		m.RepoCloneURL = p.Repository.CloneURL
	}
	return m, true, nil
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
