package linearapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// defaultAPIBaseURL is Linear's own real GraphQL/REST API base -- verified
// live against Linear's own developer documentation during §8.10's
// investigation ("Linear's GraphQL endpoint is https://api.linear.app/
// graphql"). The ONLY place this literal appears in this package;
// production wiring (cmd/control-plane/main.go) passes it explicitly,
// mirroring internal/adapters/outbound/githubapi's own defaultAPIBaseURL
// precedent exactly.
const defaultAPIBaseURL = "https://api.linear.app"

// graphQLPath is appended to apiBaseURL for every GraphQL call this
// client makes -- Linear's API is a single GraphQL endpoint, unlike
// GitHub's many REST resource paths.
const graphQLPath = "/graphql"

// maxResponseBodySize bounds how much of a Linear API response this
// client ever reads -- mirrors githubapi's own maxResponseBodySize
// reasoning: http.Client.Timeout bounds wall-clock time, not response
// size.
const maxResponseBodySize = 1 << 20 // 1 MiB

// Client makes direct calls against Linear's real GraphQL API. See this
// package's own doc.go for why this is a narrow, direct client rather
// than the general Notifier port.
type Client struct {
	httpClient *http.Client
	apiBaseURL string
}

// New builds a Client. httpClient is accepted (rather than constructed
// internally) so a caller controls its own timeout/transport, and must
// be a real client.
//
// A nil httpClient used to default to http.DefaultClient, and §30.2
// names why that default had to go: New(nil, ...) in a new package got a
// working client that no egress layer above it could see. It now yields
// one whose transport refuses every request -- the omission is useless
// rather than dangerous, and the zero value fails closed, matching
// githubapi.New's own updated convention.
//
// apiBaseURL still defaults to defaultAPIBaseURL when empty; production
// wiring should still pass it explicitly (matches githubapi.New's own
// precedent).
func New(httpClient *http.Client, apiBaseURL string) *Client {
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
	if apiBaseURL == "" {
		apiBaseURL = defaultAPIBaseURL
	}
	return &Client{
		httpClient: httpClient,
		apiBaseURL: strings.TrimSuffix(apiBaseURL, "/"),
	}
}

// graphQLRequestBody is the standard GraphQL-over-HTTP request envelope.
type graphQLRequestBody struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

// graphQLError is one entry of a GraphQL response's own top-level
// "errors" array (the real shape Linear's API, like any standard GraphQL
// server, returns on a 200 response that still failed at the field
// level -- GraphQL famously does NOT always use HTTP status codes for
// application errors).
type graphQLError struct {
	Message string `json:"message"`
}

// graphQLResponseError is returned by doGraphQL when Linear's response
// carries a non-empty top-level "errors" array, even on an HTTP 200 --
// a plain, structured error mirroring githubapi.CreatePRError's own
// "no transient/permanent classification" precedent (see that type's own
// doc comment for why: no caller of this client retries or trips a
// circuit breaker on a GraphQL error yet).
type graphQLResponseError struct {
	Messages []string
}

func (e *graphQLResponseError) Error() string {
	return fmt.Sprintf("linearapi: graphql error(s): %s", strings.Join(e.Messages, "; "))
}

// httpStatusError is returned by doGraphQL when Linear's response itself
// is a non-2xx HTTP status (auth failure, rate limit, etc. -- distinct
// from graphQLResponseError's own "200 but field-level errors" case).
type httpStatusError struct {
	Status int
	Body   string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("linearapi: http %d: %s", e.Status, e.Body)
}

// doGraphQL POSTs query/variables to {apiBaseURL}/graphql, authenticated
// with accessToken as a Bearer token (Linear's own real OAuth-app
// convention, verified live: "Authorization: Bearer <ACCESS_TOKEN>" --
// deliberately NOT the bare, un-prefixed convention Linear's SEPARATE
// personal-API-key scheme uses), and decodes the top-level "data" field
// of a successful response into out. accessToken is never logged, here or
// in any error this returns.
func (c *Client) doGraphQL(ctx context.Context, accessToken, query string, variables map[string]any, out any) error {
	reqBody, err := json.Marshal(graphQLRequestBody{Query: query, Variables: variables})
	if err != nil {
		return fmt.Errorf("linearapi: encode graphql request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBaseURL+graphQLPath, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("linearapi: build graphql request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("linearapi: graphql request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		return fmt.Errorf("linearapi: read graphql response: %w", err)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return &httpStatusError{Status: resp.StatusCode, Body: string(respBody)}
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []graphQLError  `json:"errors"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return fmt.Errorf("linearapi: decode graphql response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		messages := make([]string, len(envelope.Errors))
		for i, e := range envelope.Errors {
			messages[i] = e.Message
		}
		return &graphQLResponseError{Messages: messages}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("linearapi: decode graphql data: %w", err)
	}
	return nil
}

// refusingTransport answers every request with an error naming the cause,
// so a construction site built without a transport fails at its first call
// with something a reader can act on -- rather than silently reaching a
// customer's systems, which is what the removed default did.
type refusingTransport struct{}

func (refusingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("linearapi: this client was built with no HTTP client, so it can make no requests")
}
