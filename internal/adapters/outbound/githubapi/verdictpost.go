// This file (verdictpost.go) extends Adapter with the two real GitHub
// REST capabilities §8.2's ("server-side verdict", §8.2) verdict-
// posting tool needs beyond what this package already had (PostIssueComment,
// adapter.go): submitting a FORMAL pull request review (the "formal-review
// gate", §8.2's own "submitting an actual GitHub PR review rather
// than a comment"), and syncing the review:*-risk label vocabulary
// (internal/domain/reviewpost's own ComputeLabelSync) onto the PR.
//
// Both are plain, narrow REST calls, mirroring every existing method in
// this package: doPost/doGet for the request/response shape, APIError for
// any non-2xx status, no typed transient/permanent classification (this
// port's own established "no caller retries or trips a circuit breaker on
// this kind of failure" precedent, adapter.go's own CreatePRError doc
// comment) -- the outbox delivery worker's own attempt-count-driven
// backoff (§5.1) is what retries a failed Deliver call, not this adapter.

package githubapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/narvidev/narvi/internal/domain/reviewpost"
)

// createReviewRequest is the body POSTed to
// /repos/{owner}/{repo}/pulls/{pull_number}/reviews (real GitHub REST API
// shape -- https://docs.github.com/rest/pulls/reviews#create-a-review-for-a-pull-request).
// event is one of GitHub's own three review-event strings; this adapter
// only ever sends the two internal/domain/reviewpost.FormalReviewEvent
// produces (COMMENT/REQUEST_CHANGES) -- see that type's own doc comment
// for why APPROVE is never one of them.
type createReviewRequest struct {
	Body  string `json:"body"`
	Event string `json:"event"`
}

// CreateReview submits a formal pull request review on repo owner/repo's
// PR prNumber (§8.2's own "formal-review gate"), authenticated with
// token as a Bearer token. event is one of internal/domain/reviewpost.
// FormalReviewEventComment/FormalReviewEventRequestChanges (the caller's
// own reviewpost.ComputeFormalReviewEvent result) -- this method itself
// does not validate event further, mirroring PostIssueComment's own
// "agnostic to whose token/what body it's handed" precedent; the caller
// (internal/adapters/inbound/httpapi/reviewverdict.go) is what constructs
// event via that pure function, never a hand-typed string.
func (a *Adapter) CreateReview(ctx context.Context, owner, repo string, prNumber int, token string, event reviewpost.FormalReviewEvent, body string) error {
	reqBody, err := json.Marshal(createReviewRequest{Body: body, Event: string(event)})
	if err != nil {
		return fmt.Errorf("githubapi: encode create-review request: %w", err)
	}

	path := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/reviews", a.apiBaseURL, url.PathEscape(owner), url.PathEscape(repo), prNumber)
	if _, err := a.doPost(ctx, path, token, reqBody); err != nil {
		return fmt.Errorf("githubapi: create review: %w", err)
	}
	return nil
}

// addLabelsRequest is the body POSTed to
// /repos/{owner}/{repo}/issues/{issue_number}/labels (real GitHub REST API
// shape -- https://docs.github.com/rest/issues/labels#add-labels-to-an-issue)
// -- a pull request is addressable through the Issues API for labels
// exactly like it already is for comments (issueCommentRequest's own doc
// comment, adapter.go).
type addLabelsRequest struct {
	Labels []string `json:"labels"`
}

// AddLabels adds labels to repo owner/repo's PR prNumber -- additive only
// (GitHub's own "add labels" endpoint never removes an existing label);
// adding a label the PR already carries is a harmless no-op on GitHub's
// own side.
func (a *Adapter) AddLabels(ctx context.Context, owner, repo string, prNumber int, token string, labels []string) error {
	if len(labels) == 0 {
		return nil
	}

	reqBody, err := json.Marshal(addLabelsRequest{Labels: labels})
	if err != nil {
		return fmt.Errorf("githubapi: encode add-labels request: %w", err)
	}

	path := fmt.Sprintf("%s/repos/%s/%s/issues/%d/labels", a.apiBaseURL, url.PathEscape(owner), url.PathEscape(repo), prNumber)
	if _, err := a.doPost(ctx, path, token, reqBody); err != nil {
		return fmt.Errorf("githubapi: add labels: %w", err)
	}
	return nil
}

// RemoveLabel removes exactly one label from repo owner/repo's PR
// prNumber (real GitHub REST API shape --
// https://docs.github.com/rest/issues/labels#remove-a-label-from-an-issue,
// DELETE /repos/{owner}/{repo}/issues/{issue_number}/labels/{name}).
// Idempotent: a 404 (the label was never present, or a concurrent caller
// already removed it) is treated as success, never an error -- exactly
// the SAME "expected absence, not a failure" discipline
// ResolveContractsFingerprint's own doc comment already establishes for a
// 404 elsewhere in this package. internal/domain/reviewpost.
// ComputeLabelSync's own Remove list is built from labels the caller
// already observed present moments earlier, but a concurrent edit to the
// SAME PR (another sync, or a human) between that read and this write is
// always possible -- this method must tolerate it rather than surface a
// spurious delivery failure the outbox worker would otherwise retry
// forever.
func (a *Adapter) RemoveLabel(ctx context.Context, owner, repo string, prNumber int, token, label string) error {
	path := fmt.Sprintf("%s/repos/%s/%s/issues/%d/labels/%s",
		a.apiBaseURL, url.PathEscape(owner), url.PathEscape(repo), prNumber, url.PathEscape(label))

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return fmt.Errorf("githubapi: build remove-label request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("githubapi: remove label request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		return fmt.Errorf("githubapi: read remove-label response: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		// Already absent -- a successful no-op, not an error (see this
		// method's own doc comment).
		return nil
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		message := "no error body"
		var parsed githubErrorBody
		if jsonErr := json.Unmarshal(body, &parsed); jsonErr == nil && parsed.Message != "" {
			message = parsed.Message
		} else if len(body) > 0 {
			message = "error body did not match GitHub's expected error envelope"
		}
		return fmt.Errorf("githubapi: remove label: %w", &APIError{Status: resp.StatusCode, Message: message})
	}
	return nil
}

// labelResponse is the subset of GitHub's own real label resource shape
// this adapter needs (https://docs.github.com/rest/issues/labels#list-labels-for-an-issue)
// -- just the name; nothing here consumes color/description.
type labelResponse struct {
	Name string `json:"name"`
}

// ListLabels lists repo owner/repo's PR prNumber's own CURRENT labels
// (real GitHub REST API shape -- GET
// /repos/{owner}/{repo}/issues/{issue_number}/labels) -- the input
// internal/domain/reviewpost.ComputeLabelSync needs to decide what to
// add/remove; this method never interprets the result itself.
func (a *Adapter) ListLabels(ctx context.Context, owner, repo string, prNumber int, token string) ([]string, error) {
	path := fmt.Sprintf("%s/repos/%s/%s/issues/%d/labels", a.apiBaseURL, url.PathEscape(owner), url.PathEscape(repo), prNumber)
	body, err := a.doGet(ctx, path, token)
	if err != nil {
		return nil, fmt.Errorf("githubapi: list labels: %w", err)
	}

	var parsed []labelResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("githubapi: decode list-labels response: %w", err)
	}

	names := make([]string, len(parsed))
	for i, l := range parsed {
		names[i] = l.Name
	}
	return names, nil
}
