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

// repoInfoResponse is the subset of GitHub's real GET /repos/{owner}/{repo}
// response shape this adapter needs (https://docs.github.com/rest/repos/repos#get-a-repository).
type repoInfoResponse struct {
	DefaultBranch string `json:"default_branch"`
}

// commitResponse is the subset of GitHub's real GET
// /repos/{owner}/{repo}/commits/{ref} response shape this adapter needs
// (https://docs.github.com/rest/commits/commits#get-a-commit) -- the
// top-level "sha" field IS the commit's own SHA for whatever ref was
// requested (a branch name resolves to its current HEAD commit).
type commitResponse struct {
	SHA string `json:"sha"`
}

// ResolveBranchSHAError is the error ResolveBranchSHA returns for any
// non-2xx GitHub response -- mirrors CreatePRError exactly (a plain,
// structured error; see ports.SourceControl's own doc comment for why
// neither method invents a transient/permanent classification).
type ResolveBranchSHAError struct {
	Status  int
	Message string
}

func (e *ResolveBranchSHAError) Error() string {
	return fmt.Sprintf("githubapi: resolve branch sha: http %d: %s", e.Status, e.Message)
}

// doGet performs one authenticated GET against a.apiBaseURL+path, returning
// the raw response body on any 2xx status -- the shared request-building/
// auth/bounded-read/error-envelope-parse logic CreatePR's own inline block
// duplicates once; factored out here since ResolveBranchSHA below needs the
// IDENTICAL sequence twice (repo info, then a commit) rather than a THIRD
// verbatim copy of it.
func (a *Adapter) doGet(ctx context.Context, path, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("githubapi: build get request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("githubapi: get request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		return nil, fmt.Errorf("githubapi: read get response: %w", err)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		message := "no error body"
		var parsed githubErrorBody
		if err := json.Unmarshal(body, &parsed); err == nil && parsed.Message != "" {
			message = parsed.Message
		} else if len(body) > 0 {
			message = "error body did not match GitHub's expected error envelope"
		}
		return nil, &ResolveBranchSHAError{Status: resp.StatusCode, Message: message}
	}

	return body, nil
}

// ResolveBranchSHA implements ports.SourceControl (Step 26, "image
// builds"): resolves spec.Branch's current commit SHA via a real GET
// https://api.github.com/repos/{owner}/{repo}/commits/{branch} call. When
// spec.Branch is empty ("the repo's own default branch" -- ports.
// ResolveBranchSHASpec's own doc comment), this FIRST resolves the repo's
// real default_branch via GET https://api.github.com/repos/{owner}/{repo},
// then uses THAT as the ref -- "main"/"master" is never hardcoded or
// guessed.
//
// This is the concrete implementation of the design decision already made
// for this Step: the control plane resolves each repo's own current SHA
// directly via a real GitHub API call, BEFORE assembling a spawn's
// CreateSpec -- not by waiting for a sandbox to report anything back (that
// would need a new wire message; deliberately not built, see this Step's
// own PR description).
func (a *Adapter) ResolveBranchSHA(ctx context.Context, spec ports.ResolveBranchSHASpec) (string, error) {
	branch := spec.Branch
	if branch == "" {
		repoPath := fmt.Sprintf("%s/repos/%s/%s", a.apiBaseURL, spec.Owner, spec.Repo)
		body, err := a.doGet(ctx, repoPath, spec.Token)
		if err != nil {
			return "", fmt.Errorf("githubapi: resolve default branch: %w", err)
		}

		var repoInfo repoInfoResponse
		if err := json.Unmarshal(body, &repoInfo); err != nil {
			return "", fmt.Errorf("githubapi: decode repo info response: %w", err)
		}
		if repoInfo.DefaultBranch == "" {
			return "", fmt.Errorf("githubapi: repo %s/%s reported an empty default_branch", spec.Owner, spec.Repo)
		}
		branch = repoInfo.DefaultBranch
	}

	commitPath := fmt.Sprintf("%s/repos/%s/%s/commits/%s", a.apiBaseURL, spec.Owner, spec.Repo, branch)
	body, err := a.doGet(ctx, commitPath, spec.Token)
	if err != nil {
		return "", fmt.Errorf("githubapi: resolve commit sha for branch %q: %w", branch, err)
	}

	var commit commitResponse
	if err := json.Unmarshal(body, &commit); err != nil {
		return "", fmt.Errorf("githubapi: decode commit response: %w", err)
	}
	if commit.SHA == "" {
		return "", fmt.Errorf("githubapi: repo %s/%s branch %q reported an empty commit sha", spec.Owner, spec.Repo, branch)
	}

	return commit.SHA, nil
}
