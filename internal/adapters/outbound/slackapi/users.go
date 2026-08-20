// This file (users.go) implements §13.2's ("identities + full RBAC",
// §13.2) own auto-link algorithm need: "Fetch the actor's profile email
// from the provider API." -- a real users.info call against Slack's Web
// API, verified against Slack's own current documentation
// (docs.slack.dev/reference/methods/users.info) during this Step's
// investigation: GET/POST users.info?user=<id>, bot-token authenticated,
// requires the users:read.email OAuth scope to see profile.email at all
// (an app without that scope gets a 200/ok:true response with profile.email
// simply absent -- this client cannot distinguish that configuration gap
// from "this user genuinely has no email set" and does not try to; both
// surface as ok=false, "" to GetUserEmail's own caller).
//
// Called from internal/app/identitylink.Resolve, NEVER from the outbox
// delivery worker's own Notifier.Deliver path -- a distinct call site from
// this package's own chat.postMessage Client.Deliver (see this package's
// own doc.go for why this Client is already, deliberately, a SEPARATE
// small client from internal/adapters/inbound/slack's own ack.go).

package slackapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// usersInfoResponse is the subset of Slack's real users.info response body
// this client reads (verified live against Slack's own current docs).
// Slack's own universal envelope (Ok/Error) is shared with
// postMessageResponse (client.go) but kept as its own separate type here
// rather than reused -- the two responses carry structurally different
// payloads beyond that shared envelope, and duplicating this one small
// pair of fields is simpler than threading a generic envelope type through
// two otherwise-unrelated response shapes.
type usersInfoResponse struct {
	Ok    bool   `json:"ok"`
	Error string `json:"error"`
	User  struct {
		Profile struct {
			Email string `json:"email"`
		} `json:"profile"`
	} `json:"user"`
}

// ErrSlackUserNotFound is Slack's own real users.info error code for a
// user id that doesn't exist (or is no longer visible to this app) --
// verified against Slack's own docs. This is the ONE users.info error this
// client treats as PERMANENT (wrapped via platform.Permanent by its own
// caller, internal/app/identitylink.Resolve, which is the one that
// actually calls platform.Retry) -- every other non-ok response (rate
// limiting, a transient 5xx, a network error) is left unwrapped, since
// §13.2 explicitly requires a fetch FAILURE to be retried, never treated
// as "this identity simply has no email".
var ErrSlackUserNotFound = errors.New("slackapi: user_not_found")

// GetUserEmail calls users.info for userID and returns the user's profile
// email. ok=false, err=nil means Slack answered successfully but this
// user simply has no email visible to this app (either a genuinely unset
// profile field, or a missing users:read.email scope -- indistinguishable
// from this response alone, and both mean the SAME thing to this call's
// own caller: no email to match against, do not guess). A non-nil error
// means the call itself failed -- ErrSlackUserNotFound for Slack's own
// "no such user" response (see that sentinel's own doc comment for why
// this is the one case NOT meant to be retried), any other error for
// everything else (network failure, an unexpected non-ok response, a
// malformed response body), all of which the caller should treat as
// retryable.
func (c *Client) GetUserEmail(ctx context.Context, userID string) (email string, ok bool, err error) {
	reqURL := c.apiBaseURL + "/users.info?" + url.Values{"user": {userID}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", false, fmt.Errorf("slackapi: build users.info request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.botToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("slackapi: users.info request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		return "", false, fmt.Errorf("slackapi: read users.info response: %w", err)
	}

	var parsed usersInfoResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", false, fmt.Errorf("slackapi: decode users.info response: %w", err)
	}
	if !parsed.Ok {
		if parsed.Error == "user_not_found" {
			return "", false, ErrSlackUserNotFound
		}
		return "", false, fmt.Errorf("slackapi: users.info failed: %s", parsed.Error)
	}
	if parsed.User.Profile.Email == "" {
		return "", false, nil
	}
	return parsed.User.Profile.Email, true, nil
}
