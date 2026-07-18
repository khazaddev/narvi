package githubapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/khazaddev/narvi/internal/app/ports"
)

// defaultAPIBaseURL is GitHub's own real REST API base -- the ONLY place
// this literal appears in this package; production wiring
// (cmd/control-plane/main.go) passes it explicitly, mirroring
// internal/adapters/inbound/auth's own githubAPIBaseURL constant
// precedent exactly, so it is never silently duplicated or hardcoded a
// second time deeper in this package.
const defaultAPIBaseURL = "https://api.github.com"

// maxResponseBodySize bounds how much of a GitHub API response body this
// adapter ever reads -- mirrors internal/adapters/outbound/modal/client.
// go's own maxResponseBodySize reasoning: http.Client.Timeout bounds
// wall-clock time, not response size.
const maxResponseBodySize = 1 << 20 // 1 MiB

// Adapter implements ports.SourceControl against GitHub's real REST API.
type Adapter struct {
	httpClient *http.Client
	apiBaseURL string
}

// var _ ports.SourceControl = (*Adapter)(nil) makes a SourceControl
// signature drift a build error, not a runtime surprise.
var _ ports.SourceControl = (*Adapter)(nil)

// New builds an Adapter. httpClient is accepted (rather than constructed
// internally) so a caller can control its own timeout/transport, matching
// this port's own doc comment on testability; a nil httpClient defaults to
// http.DefaultClient. apiBaseURL defaults to defaultAPIBaseURL when empty
// -- production wiring should still pass it explicitly (see doc.go).
func New(httpClient *http.Client, apiBaseURL string) *Adapter {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if apiBaseURL == "" {
		apiBaseURL = defaultAPIBaseURL
	}
	return &Adapter{
		httpClient: httpClient,
		apiBaseURL: strings.TrimSuffix(apiBaseURL, "/"),
	}
}

// createPRRequest is the body POSTed to /repos/{owner}/{repo}/pulls (real
// GitHub REST API shape -- https://docs.github.com/rest/pulls/pulls#create-a-pull-request,
// verified against GitHub's own live documentation during this Step's own
// design phase).
type createPRRequest struct {
	Title string `json:"title"`
	Head  string `json:"head"`
	Base  string `json:"base"`
	Body  string `json:"body"`
}

// createPRResponse is the subset of GitHub's real pull-request response
// shape this adapter needs.
type createPRResponse struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
}

// githubErrorBody is GitHub's own real error envelope shape on a non-2xx
// response (a top-level "message", optionally more detail in "errors").
type githubErrorBody struct {
	Message string `json:"message"`
}

// CreatePRError is the error CreatePR returns for any non-2xx GitHub
// response -- a plain, structured error (this port does not invent a
// transient/permanent classification the way ports.ProviderError does;
// see ports.SourceControl's own doc comment for why: no caller of this
// port retries or trips a circuit breaker on a PR-creation failure yet).
type CreatePRError struct {
	Status  int
	Message string
}

func (e *CreatePRError) Error() string {
	return fmt.Sprintf("githubapi: create pr: http %d: %s", e.Status, e.Message)
}

// CreatePR implements ports.SourceControl: a real POST
// https://api.github.com/repos/{owner}/{repo}/pulls call, authenticated
// with spec.Token as a GitHub OAuth user access token (Authorization:
// Bearer <token> -- GitHub's REST API accepts a user OAuth token this
// way). spec.Token is never logged, here or anywhere this error might
// surface to (CreatePRError carries only GitHub's own response message,
// never the request itself).
func (a *Adapter) CreatePR(ctx context.Context, spec ports.CreatePRSpec) (ports.PRRef, error) {
	reqBody, err := json.Marshal(createPRRequest{
		Title: spec.Title,
		Head:  spec.Head,
		Base:  spec.Base,
		Body:  spec.Body,
	})
	if err != nil {
		return ports.PRRef{}, fmt.Errorf("githubapi: encode create-pr request: %w", err)
	}

	path := fmt.Sprintf("%s/repos/%s/%s/pulls", a.apiBaseURL, spec.Owner, spec.Repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(reqBody))
	if err != nil {
		return ports.PRRef{}, fmt.Errorf("githubapi: build create-pr request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+spec.Token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return ports.PRRef{}, fmt.Errorf("githubapi: create-pr request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		return ports.PRRef{}, fmt.Errorf("githubapi: read create-pr response: %w", err)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		message := "no error body"
		var parsed githubErrorBody
		if err := json.Unmarshal(respBody, &parsed); err == nil && parsed.Message != "" {
			message = parsed.Message
		} else if len(respBody) > 0 {
			message = "error body did not match GitHub's expected error envelope"
		}
		return ports.PRRef{}, &CreatePRError{Status: resp.StatusCode, Message: message}
	}

	var parsed createPRResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return ports.PRRef{}, fmt.Errorf("githubapi: decode create-pr response: %w", err)
	}

	return ports.PRRef{Number: parsed.Number, URL: parsed.HTMLURL}, nil
}
