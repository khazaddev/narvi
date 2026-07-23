// This file (ack.go) implements the single, direct Slack API call this
// Step's own in-thread ack needs (doc.go's own "in-thread acks --
// scoping decision") -- a tiny, unexported chat.postMessage client, NOT
// the general Notifier/outbox abstraction Step 35 builds.

package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// defaultSlackAPIBaseURL is Slack's own real Web API base -- the ONLY
// place this literal appears in this package; production wiring
// (cmd/control-plane/main.go) passes it explicitly, mirroring
// internal/adapters/outbound/githubapi's own defaultAPIBaseURL/apiBaseURL
// constructor-parameter precedent exactly, so a test can override it with
// a local httptest.Server standing in for Slack's real API.
const defaultSlackAPIBaseURL = "https://slack.com/api"

// maxAckResponseBodySize bounds how much of Slack's own chat.postMessage
// response body this client ever reads -- mirrors githubapi's own
// maxResponseBodySize/modal's own identical precedent.
const maxAckResponseBodySize = 1 << 20 // 1 MiB

// ackClient posts exactly one Slack message per call (chat.postMessage) --
// used only for this Step's own in-thread ack, never a queued/retried
// delivery.
type ackClient struct {
	httpClient *http.Client
	apiBaseURL string
	botToken   string
}

// newAckClient builds an ackClient. httpClient defaults to
// http.DefaultClient when nil; apiBaseURL defaults to
// defaultSlackAPIBaseURL when empty -- production wiring should still
// pass it explicitly (cmd/control-plane/main.go).
func newAckClient(httpClient *http.Client, apiBaseURL, botToken string) *ackClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if apiBaseURL == "" {
		apiBaseURL = defaultSlackAPIBaseURL
	}
	return &ackClient{httpClient: httpClient, apiBaseURL: apiBaseURL, botToken: botToken}
}

// postMessageRequest is the subset of Slack's real chat.postMessage
// request body (docs.slack.dev/reference/methods/chat.postMessage,
// confirmed at this Step's own design time) this client needs: channel +
// text, threaded via thread_ts.
type postMessageRequest struct {
	Channel  string `json:"channel"`
	ThreadTS string `json:"thread_ts"`
	Text     string `json:"text"`
}

// postMessageResponse is Slack Web API's own universal response envelope
// shape -- every method responds HTTP 200 even on an API-level failure,
// with "ok": false and an "error" code naming what went wrong (Slack's
// own documented Web API convention) -- so success is determined by
// parsing Ok, never by HTTP status alone.
type postMessageResponse struct {
	Ok    bool   `json:"ok"`
	Error string `json:"error"`
}

// PostAckError is returned by postAck for a non-ok Slack API response --
// a distinct, named error (mirroring githubapi's own CreatePRError/
// APIError precedent) so a caller can log Slack's own real error code,
// never a bare "request failed".
type PostAckError struct {
	SlackError string
}

func (e *PostAckError) Error() string {
	return fmt.Sprintf("slack: chat.postMessage failed: %s", e.SlackError)
}

// postAck posts text into channel, threaded under threadTS, using the
// bot token this client was built with (Authorization: Bearer -- Slack's
// own documented bot-token auth scheme for Web API calls).
func (c *ackClient) postAck(ctx context.Context, channel, threadTS, text string) error {
	reqBody, err := json.Marshal(postMessageRequest{Channel: channel, ThreadTS: threadTS, Text: text})
	if err != nil {
		return fmt.Errorf("slack: encode chat.postMessage request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBaseURL+"/chat.postMessage", bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("slack: build chat.postMessage request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+c.botToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("slack: chat.postMessage request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxAckResponseBodySize))
	if err != nil {
		return fmt.Errorf("slack: read chat.postMessage response: %w", err)
	}

	var parsed postMessageResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return fmt.Errorf("slack: decode chat.postMessage response: %w", err)
	}
	if !parsed.Ok {
		return &PostAckError{SlackError: parsed.Error}
	}
	return nil
}

// postAckBounded calls postAck under a derived context bounded by timeout --
// postAck is a genuine outbound network call made synchronously in the
// inbound webhook handler's own request path (handler.go), which otherwise
// carries no deadline of its own (a bare r.Context()), so every caller in
// this package must go through this wrapper rather than calling postAck
// directly. Mirrors internal/app/sessionactor/pushpr.go's own
// PRCreateTimeout-bounded CreatePR call precedent exactly. A zero or
// negative timeout would make context.WithTimeout expire the call
// immediately, so callers must always pass a real, positive value
// (production wiring passes platform.Timeouts.SlackAckTimeout).
func (c *ackClient) postAckBounded(ctx context.Context, timeout time.Duration, channel, threadTS, text string) error {
	ackCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return c.postAck(ackCtx, channel, threadTS, text)
}
