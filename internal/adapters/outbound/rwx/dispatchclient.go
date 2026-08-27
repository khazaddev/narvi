package rwx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/khazaddev/narvi/internal/platform"
)

// defaultDispatchBaseURL is RWX's own real Dispatches API host — the ONLY
// place this literal appears in this package; production wiring
// (cmd/control-plane/main.go) may override it via NewDispatchClient's own
// baseURL parameter, mirroring githubapi.defaultAPIBaseURL's identical
// "one named constant, override-able for tests" precedent.
const defaultDispatchBaseURL = "https://cloud.rwx.com"

// maxDispatchResponseBodySize bounds how much of a Dispatches API response
// body this client ever reads — mirrors modal's/githubapi's own identical
// maxResponseBodySize reasoning: http.Client.Timeout (or a caller's own
// context deadline) bounds wall-clock time, not response size.
const maxDispatchResponseBodySize = 1 << 20 // 1 MiB

// DispatchClient is a small, real HTTP client for RWX's one documented
// Dispatches API (§4.1.1: "POST https://cloud.rwx.com/mint/api/runs/
// dispatches ... GET .../dispatches/:dispatch_id"), used ONLY by the PR
// preview mechanism (§4.1.2) — never sandbox lifecycle, which is the
// separate, CLI-based Provider (provider.go). Unlike Provider, this is a
// plain, documented REST call, genuinely testable against a fake
// httptest.Server (dispatchclient_test.go), the same way every other
// outbound adapter's own HTTP surface in this codebase already is.
type DispatchClient struct {
	httpClient  *http.Client
	baseURL     string
	accessToken string
}

// NewDispatchClient builds a DispatchClient. httpClient defaults to
// http.DefaultClient when nil (mirroring githubapi.New's own identical
// convention: each call is bounded by its own caller-supplied context
// deadline, never a package-level http.Client.Timeout — here, the caller
// is always outboxworker.Builder.attempt, whose own platform.Timeouts.
// OutboxDeliveryTimeout already wraps every ports.Notifier.Deliver call,
// see notifier.go's own doc comment). baseURL defaults to RWX's real
// Dispatches API host when empty.
func NewDispatchClient(httpClient *http.Client, baseURL, accessToken string) *DispatchClient {
	if httpClient == nil {
		// Not http.DefaultClient. §30.2 calls that default an attractive
		// nuisance and removes it from all four outbound constructors:
		// New(nil, ...) in a new package used to produce a WORKING client
		// that no egress layer could see. It now produces one that can
		// make no request at all -- the omission is useless rather than
		// dangerous, and the zero value fails closed.
		//
		// This surface's own gate is a later Step's work; the default had
		// to go now regardless, because a gate installed later cannot
		// reach a client somebody already constructed around it.
		httpClient = &http.Client{Transport: refusingTransport{}}
	}
	if baseURL == "" {
		baseURL = defaultDispatchBaseURL
	}
	return &DispatchClient{httpClient: httpClient, baseURL: strings.TrimSuffix(baseURL, "/"), accessToken: accessToken}
}

// dispatchRequest is the body POSTed to /mint/api/runs/dispatches — RWX's
// own documented shape (§4.1.1: "JSON body {key, params, ref, title}").
type dispatchRequest struct {
	Key    string            `json:"key"`
	Params map[string]string `json:"params"`
	Ref    string            `json:"ref"`
	Title  string            `json:"title,omitempty"`
}

// dispatchResponse is RWX's own documented 201 response shape (§4.1.1:
// "201 -> {dispatch_id}").
type dispatchResponse struct {
	DispatchID string `json:"dispatch_id"`
}

// Dispatch triggers one RWX preview build via POST /mint/api/runs/
// dispatches (§4.1.2 point 2: "ref = the pushed sha, key = the repo's own
// dispatchKey, params = {pr-number, head-sha, session-id}"). Returns RWX's
// own dispatch_id on success. Delivery is this ONE POST only — it never
// polls GET .../dispatches/:id to completion (§4.1.2 point 2: "it never
// waits for the build"); that polling endpoint exists in RWX's own API
// but has no caller in this Step's own v1 scope (§4.1.2 point 4: the
// friendly URL is deterministic at enqueue time, "no build to await
// inside a Deliver" — the v2 upgrade path if template drift ever bites).
func (c *DispatchClient) Dispatch(ctx context.Context, key, ref, title string, params map[string]string) (string, error) {
	reqBody, err := json.Marshal(dispatchRequest{Key: key, Params: params, Ref: ref, Title: title})
	if err != nil {
		return "", fmt.Errorf("rwx: encode dispatch request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/mint/api/runs/dispatches", bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("rwx: build dispatch request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	if correlationID, ok := platform.CorrelationIDFromContext(ctx); ok && correlationID != "" {
		req.Header.Set(platform.CorrelationIDHeader, correlationID)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", classifyDispatchNetworkError(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDispatchResponseBodySize))
	if err != nil {
		return "", fmt.Errorf("rwx: read dispatch response: %w", err)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", classifyDispatchErrorResponse(resp.StatusCode, body)
	}

	var parsed dispatchResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("rwx: decode dispatch response: %w", err)
	}
	return parsed.DispatchID, nil
}

// refusingTransport answers every request with an error naming the cause,
// so a construction site built without a transport fails at its first call
// with something a reader can act on -- rather than silently reaching a
// customer's systems, which is what the removed default did.
type refusingTransport struct{}

func (refusingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("rwx: this client was built with no HTTP client, so it can make no requests")
}
