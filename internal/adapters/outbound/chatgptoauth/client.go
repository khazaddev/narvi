package chatgptoauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is auth.openai.com's real base -- the ONLY place this
// literal appears in this package; production wiring (cmd/control-plane/
// main.go) passes it explicitly, mirroring internal/adapters/outbound/
// githubapi's own defaultAPIBaseURL precedent exactly.
const DefaultBaseURL = "https://auth.openai.com"

// ClientID is Codex CLI's own public OAuth client id -- OpenCode reuses it
// verbatim ("originator=opencode", §29.2). Public by design (PKCE, no
// client secret): safe as a compile-time constant, not a secret needing
// encryption-at-rest the way a resolved credential value does.
const ClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

// VerificationURL is the FIXED, well-known URL a human opens on ANY
// device to enter their own user code (§29.2/§29.3) -- never returned
// dynamically by StartDeviceAuth's own response; a literal constant here
// so the Settings UI never has to hardcode it a second time independently
// (mirrors this Step's own ChatGPTLinkStatus.verificationUrl wire field,
// which is populated from this exact constant).
const VerificationURL = "https://auth.openai.com/codex/device"

// deviceAuthCallbackRedirectURI is the fixed redirect_uri the device
// flow's own authorization-code exchange uses (§29.2: "redirect_
// uri=https://auth.openai.com/deviceauth/callback") -- distinct from the
// browser method's own localhost:1455 redirect (§29.2's own negative
// finding: THAT redirect is why a Narvi-hosted web callback is impossible
// for the browser method; the device method Narvi actually uses has its
// own, different, fixed redirect entirely under auth.openai.com's own
// domain).
const deviceAuthCallbackRedirectURI = "https://auth.openai.com/deviceauth/callback"

// maxResponseBodySize bounds how much of an auth.openai.com response body
// this package ever reads -- mirrors githubapi/modal/opencode's own
// identical precedent (http.Client's own Timeout bounds wall-clock time,
// not response size).
const maxResponseBodySize = 1 << 20 // 1 MiB

// ErrDeviceAuthPending is PollDeviceToken's own sentinel for "not granted
// yet" (§29.2: "403/404 = pending") -- an ordinary, expected, retryable
// outcome for a still-unapproved device code, never logged or surfaced as
// a real failure by this package's own callers (internal/app/chatgptlink).
var ErrDeviceAuthPending = errors.New("chatgptoauth: device authorization pending")

// Client is the CP's own direct outbound client for the ChatGPT-account
// device-authorization flow (§29.2/§29.9) -- a plain *http.Client wrapper,
// structurally mirroring githubapi.Adapter/modal's own "constructor-
// injected client + base URL, one shared timeout field" shape (see
// package doc.go).
type Client struct {
	httpClient *http.Client
	baseURL    string
	clientID   string
	timeout    time.Duration
}

// New builds a Client. httpClient is accepted (rather than constructed
// internally) so a caller can control its own transport, mirroring
// githubapi.New's own testability precedent; a nil httpClient defaults to
// http.DefaultClient. baseURL defaults to DefaultBaseURL when empty.
// timeout bounds EVERY call this Client makes (platform.Timeouts.
// ChatGPTOAuthHTTPClientTimeout in production) via a fresh
// context.WithTimeout per call, mirroring internal/adapters/outbound/
// opencode's own doJSONTimeout precedent -- never http.Client.Timeout
// itself, so a caller-supplied ctx deadline and this package's own bound
// compose the same way opencode's adapter already does.
func New(httpClient *http.Client, baseURL string, timeout time.Duration) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		httpClient: httpClient,
		baseURL:    strings.TrimSuffix(baseURL, "/"),
		clientID:   ClientID,
		timeout:    timeout,
	}
}

// UsercodeResult is StartDeviceAuth's own result.
type UsercodeResult struct {
	DeviceAuthID string
	UserCode     string
	// Interval is the server-provided minimum gap between poll attempts
	// (§29.3 point 2) -- the CALLER (internal/app/chatgptlink) is
	// responsible for actually throttling to it, via chatgpt_link_
	// attempts.last_polled_at; this package makes no polling decisions of
	// its own.
	Interval time.Duration
}

// StartDeviceAuth is call 1 of 4 (§29.2): POST /api/accounts/deviceauth/
// usercode {client_id}.
func (c *Client) StartDeviceAuth(ctx context.Context) (UsercodeResult, error) {
	var resp usercodeResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/accounts/deviceauth/usercode", usercodeRequest{ClientID: c.clientID}, &resp); err != nil {
		return UsercodeResult{}, fmt.Errorf("chatgptoauth: start device auth: %w", err)
	}
	if resp.DeviceAuthID == "" || resp.UserCode == "" {
		return UsercodeResult{}, fmt.Errorf("chatgptoauth: start device auth: response carried no device_auth_id/user_code")
	}
	return UsercodeResult{
		DeviceAuthID: resp.DeviceAuthID,
		UserCode:     resp.UserCode,
		Interval:     time.Duration(resp.Interval) * time.Second,
	}, nil
}

// DeviceTokenResult is PollDeviceToken's own result on grant.
type DeviceTokenResult struct {
	AuthorizationCode string
	CodeVerifier      string
}

// PollDeviceToken is call 2 of 4 (§29.2): POST /api/accounts/deviceauth/
// token {device_auth_id, user_code} -- one attempt, no internal retry
// loop (the CALLER, internal/app/chatgptlink, owns the "at most one
// upstream attempt per server-provided interval" throttling, §29.3 point
// 2; this package's own job is translating exactly ONE HTTP attempt's
// outcome). Returns ErrDeviceAuthPending on 403/404 (§29.2's own verified
// "403/404 = pending" finding) -- every other non-2xx is a real error.
func (c *Client) PollDeviceToken(ctx context.Context, deviceAuthID, userCode string) (DeviceTokenResult, error) {
	var resp deviceTokenResponse
	status, err := c.doJSONStatus(ctx, http.MethodPost, "/api/accounts/deviceauth/token",
		deviceTokenRequest{DeviceAuthID: deviceAuthID, UserCode: userCode}, &resp)
	if err != nil {
		if status == http.StatusForbidden || status == http.StatusNotFound {
			return DeviceTokenResult{}, ErrDeviceAuthPending
		}
		return DeviceTokenResult{}, fmt.Errorf("chatgptoauth: poll device token: %w", err)
	}
	if resp.AuthorizationCode == "" || resp.CodeVerifier == "" {
		return DeviceTokenResult{}, fmt.Errorf("chatgptoauth: poll device token: response carried no authorization_code/code_verifier")
	}
	return DeviceTokenResult{AuthorizationCode: resp.AuthorizationCode, CodeVerifier: resp.CodeVerifier}, nil
}

// TokenResult is /oauth/token's own translated result, for BOTH grant
// types this package uses.
type TokenResult struct {
	AccessToken  string
	RefreshToken string
	// ExpiresIn is the token-response's own expires_in duration, NOT an
	// absolute timestamp -- this package does no time.Now() arithmetic of
	// its own (a pure wire-shape translator); the caller (internal/app/
	// chatgptlink, internal/app/chatgptrefresh) computes the absolute
	// oauth_expires_at using its own clock.
	ExpiresIn time.Duration
	AccountID string
}

// ExchangeAuthorizationCode is call 3 of 4 (§29.2): POST /oauth/token
// (grant_type=authorization_code, redirect_uri=deviceAuthCallbackRedirectURI,
// code=<authorizationCode from PollDeviceToken>, code_verifier=<from the
// SAME call>, client_id).
func (c *Client) ExchangeAuthorizationCode(ctx context.Context, authorizationCode, codeVerifier string) (TokenResult, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {authorizationCode},
		"code_verifier": {codeVerifier},
		"redirect_uri":  {deviceAuthCallbackRedirectURI},
		"client_id":     {c.clientID},
	}
	return c.postToken(ctx, form)
}

// RefreshToken is call 4 of 4 (§29.2): POST /oauth/token
// (grant_type=refresh_token, refresh_token, client_id) -- returns a NEW
// access token AND a new refresh token that REPLACES the old one
// (rotation, §29.2) -- the caller (internal/app/chatgptrefresh) is
// responsible for atomically overwriting the stored pair with this
// result, never retaining the consumed refresh token.
func (c *Client) RefreshToken(ctx context.Context, refreshToken string) (TokenResult, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {c.clientID},
	}
	return c.postToken(ctx, form)
}

// postToken is the shared body of ExchangeAuthorizationCode/RefreshToken:
// both are POST /oauth/token, differing only in their own form fields.
// Standard RFC 6749 §4 form-encoding -- see package doc.go's own "verified
// vs. inferred" section for why (not JSON, unlike the two custom
// /api/accounts/deviceauth/* calls above).
func (c *Client) postToken(ctx context.Context, form url.Values) (TokenResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return TokenResult{}, fmt.Errorf("chatgptoauth: build /oauth/token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return TokenResult{}, fmt.Errorf("chatgptoauth: POST /oauth/token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		return TokenResult{}, fmt.Errorf("chatgptoauth: POST /oauth/token: read response body: %w", err)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		var tokenErr tokenErrorResponse
		// Best-effort: an unparseable error body still surfaces a real
		// *TokenError with an empty Code (IsTerminal() then correctly
		// reports false -- see that method's own doc comment, "unrecognized
		// stays transient by default").
		_ = json.Unmarshal(body, &tokenErr)
		return TokenResult{}, &TokenError{
			StatusCode:  resp.StatusCode,
			Code:        tokenErr.Error,
			Description: tokenErr.ErrorDescription,
		}
	}

	var parsed tokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return TokenResult{}, fmt.Errorf("chatgptoauth: POST /oauth/token: decode response: %w", err)
	}
	if parsed.AccessToken == "" || parsed.RefreshToken == "" {
		return TokenResult{}, fmt.Errorf("chatgptoauth: POST /oauth/token: response carried no access_token/refresh_token")
	}

	// AccountID extraction is BEST-EFFORT, not fatal to this call --
	// deliberate, and load-bearing for RefreshToken specifically. §29.10
	// risk 7: "the pinned binary preserves stored accountId across
	// refreshes rather than re-deriving it each time; the CP refresher
	// does the same" -- i.e. a refresh_token grant's own response is not
	// guaranteed to carry a fresh, usable id_token the way the initial
	// authorization_code exchange's response does (RFC 6749 leaves
	// id_token-on-refresh provider-specific, and §29.2 never states
	// OpenAI includes one). A parse failure here therefore does NOT fail
	// the whole call -- AccountID is simply left "" and the CALLER
	// decides what that means: ExchangeAuthorizationCode's own caller
	// (internal/app/chatgptlink, a brand-new link) must treat "" as a
	// real error (there is no "stored" accountId yet to fall back to);
	// RefreshToken's own caller (internal/app/chatgptrefresh) must NEVER
	// read this field at all, always preserving the accountId it already
	// had stored from the original link, per §29.10 risk 7 verbatim.
	accountID, _ := decodeIDTokenAccountID(parsed.IDToken)

	return TokenResult{
		AccessToken:  parsed.AccessToken,
		RefreshToken: parsed.RefreshToken,
		ExpiresIn:    time.Duration(parsed.ExpiresIn) * time.Second,
		AccountID:    accountID,
	}, nil
}

// doJSON is doJSONStatus's own common case: the caller does not need the
// raw HTTP status code (used by every call except PollDeviceToken, which
// needs to distinguish 403/404 from every other failure).
func (c *Client) doJSON(ctx context.Context, method, path string, reqBody, out any) error {
	_, err := c.doJSONStatus(ctx, method, path, reqBody, out)
	return err
}

// doJSONStatus performs one JSON request/response call against the two
// custom /api/accounts/deviceauth/* endpoints (see package doc.go's own
// "verified vs. inferred" section for why these two are JSON, unlike
// /oauth/token's own form-encoding). Always returns the real HTTP status
// code alongside any error, even on failure, so PollDeviceToken can branch
// on 403/404 without a second round trip.
func (c *Client) doJSONStatus(ctx context.Context, method, path string, reqBody, out any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	encoded, err := json.Marshal(reqBody)
	if err != nil {
		return 0, fmt.Errorf("chatgptoauth: encode %s %s request: %w", method, path, err)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return 0, fmt.Errorf("chatgptoauth: build %s %s request: %w", method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("chatgptoauth: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("chatgptoauth: %s %s: read response body: %w", method, path, err)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return resp.StatusCode, fmt.Errorf("chatgptoauth: %s %s: http %s: %s", method, path, strconv.Itoa(resp.StatusCode), string(body))
	}

	if out != nil && len(body) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			return resp.StatusCode, fmt.Errorf("chatgptoauth: %s %s: decode response: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}
