// This file (prbody.go) implements ports.SourceControl.GetPRBody/
// UpdatePRBody ("review digest: description adequacy + graduated
// remediation", §26.2) -- the two real GitHub REST API calls the
// description-autofix notifier (internal/app/outboxworker) uses to
// re-fetch a pull request's own CURRENT body, then overwrite it, at
// delivery time. Both are thin, single-purpose calls, mirroring
// GetFileContent/UpdateFileContent's own identical "one lightweight GET,
// one lightweight write, the caller composes content, this adapter never
// does" shape.

package githubapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/narvidev/narvi/internal/app/ports"
)

// getPRBodyResponse is the subset of GitHub's real GET
// /repos/{owner}/{repo}/pulls/{pull_number} response shape GetPRBody
// needs -- just the body field, unlike pullRequestResponse (adapter.go),
// which decodes head/base/labels/stack for a completely different
// caller's needs. A dedicated, narrower type rather than reusing
// pullRequestResponse: adding a Body field there would give every OTHER
// caller of GetPullRequest a field it never asked for and this file would
// still need its own conversion regardless.
//
// Body is a pointer -- GitHub's own REST API documents this field as
// nullable (a PR opened with no description at all decodes "body": null,
// never an empty string).
type getPRBodyResponse struct {
	Body *string `json:"body"`
}

// GetPRBody implements ports.SourceControl (§26.2): a real GET
// https://api.github.com/repos/{owner}/{repo}/pulls/{number} call,
// authenticated with token as a Bearer token -- like GetPullRequest, this
// method is agnostic to whose token it's handed (production wiring,
// cmd/control-plane/main.go, passes the bot's own statically-configured
// credential, since the description-autofix notifier is a system-
// initiated action with no per-PR human creator to attribute a read to
// either). found=false, err=nil on a confirmed GitHub 404 (the PR does
// not exist, or is no longer reachable with this token) -- mirrors
// GetOpenPR's own identical "a confirmed-absent resource is a legitimate
// answer, never conflated with a genuine API failure" discipline.
func (a *Adapter) GetPRBody(ctx context.Context, owner, repo string, number int, token string) (string, bool, error) {
	path := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", a.apiBaseURL, url.PathEscape(owner), url.PathEscape(repo), number)
	body, err := a.doGet(ctx, path, token)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return "", false, nil
		}
		return "", false, fmt.Errorf("githubapi: get pr body: %w", err)
	}

	var parsed getPRBodyResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", false, fmt.Errorf("githubapi: decode get-pr-body response: %w", err)
	}
	if parsed.Body == nil {
		return "", true, nil
	}
	return *parsed.Body, true, nil
}

// updatePRBodyRequest is the body PATCHed to /repos/{owner}/{repo}/
// pulls/{pull_number} (real GitHub REST API shape --
// https://docs.github.com/rest/pulls/pulls#update-a-pull-request).
// Deliberately carries ONLY body -- GitHub's own documented request shape
// accepts title/state/base/maintainer_can_modify too, but this adapter
// method sends none of them, so there is nothing in the request itself
// that could ever rewrite the title (mirrors UpdatePRBodySpec's own
// "structurally impossible" framing at the port level, enforced again
// here at the wire-request level).
type updatePRBodyRequest struct {
	Body string `json:"body"`
}

// UpdatePRBody implements ports.SourceControl (§26.2): a real
// PATCH https://api.github.com/repos/{owner}/{repo}/pulls/{number} call,
// authenticated with spec.Token, setting body to spec.Body ONLY -- never
// title, never any other field this endpoint also accepts.
func (a *Adapter) UpdatePRBody(ctx context.Context, spec ports.UpdatePRBodySpec) error {
	reqBody, err := json.Marshal(updatePRBodyRequest{Body: spec.Body})
	if err != nil {
		return fmt.Errorf("githubapi: encode update-pr-body request: %w", err)
	}

	path := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", a.apiBaseURL, url.PathEscape(spec.Owner), url.PathEscape(spec.Repo), spec.Number)
	if _, err := a.doPatch(ctx, path, spec.Token, reqBody); err != nil {
		return fmt.Errorf("githubapi: update pr body: %w", err)
	}
	return nil
}
