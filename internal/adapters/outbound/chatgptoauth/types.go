package chatgptoauth

// usercodeRequest is POST /api/accounts/deviceauth/usercode's request body
// (§29.2: "POST https://auth.openai.com/api/accounts/deviceauth/usercode
// {client_id}").
type usercodeRequest struct {
	ClientID string `json:"client_id"`
}

// usercodeResponse is that same call's response. §29.2 names the shape as
// "{device_auth_id, user_code, interval}" but does not state each field's
// own JSON type; this package's own usercode canary (usercode_canary_
// test.go, §29.7's own named scheduled canary) made ONE real,
// unauthenticated call against the live production endpoint during this
// Step's own implementation and found two corrections to that shape:
// interval is a STRING (e.g. "5"), not a JSON number, and the response
// ALSO carries a fourth field, expires_at (an RFC 3339 timestamp) that
// §29.2's own field list never mentioned at all -- both verified
// directly, not inferred. expires_at is a genuine, better source of
// truth for this device code's own real expiry than any Narvi-side
// invented TTL would be, so StartDeviceAuth (client.go) uses it.
type usercodeResponse struct {
	DeviceAuthID string `json:"device_auth_id"`
	UserCode     string `json:"user_code"`
	// Interval is the server-provided minimum seconds between poll
	// attempts (§29.3 point 2: "throttled by the server-provided interval
	// via last_polled_at") -- a STRING on the wire (verified live), parsed
	// to a time.Duration by StartDeviceAuth.
	Interval string `json:"interval"`
	// ExpiresAt is this device code's own real expiry, RFC 3339 (verified
	// live: "2026-08-07T01:48:44.868061+00:00") -- NOT part of §29.2's own
	// original field list; discovered by this package's own usercode
	// canary.
	ExpiresAt string `json:"expires_at"`
}

// deviceTokenRequest is POST /api/accounts/deviceauth/token's request body
// (§29.2: "{device_auth_id, user_code}").
type deviceTokenRequest struct {
	DeviceAuthID string `json:"device_auth_id"`
	UserCode     string `json:"user_code"`
}

// deviceTokenResponse is that same call's response ON GRANT (§29.2:
// "yields {authorization_code, code_verifier}"). A 403/404 status instead
// means "pending" -- see ErrDeviceAuthPending -- and carries no body this
// package parses.
type deviceTokenResponse struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeVerifier      string `json:"code_verifier"`
}

// tokenResponse is POST /oauth/token's response, for BOTH grant types this
// package uses (§29.2: "Token material: access_token (a JWT ...), rotating
// refresh_token, expires_in, and an id_token whose chatgpt_account_id
// claim becomes the accountId the Codex backend requires per request") --
// the standard OAuth2/OIDC token-response field names, exactly as §29.2
// names them (not this package's own invention).
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	IDToken      string `json:"id_token"`
}

// tokenErrorResponse is a failed /oauth/token call's own error body --
// the standard OAuth2 error shape (RFC 6749 §5.2: {"error", "error_
// description"}). §29.2 names two concrete `error` values this package
// treats as terminal: "invalid_grant" (a bad/expired/revoked grant) and
// "refresh_token_reused" (OpenAI's own reuse-detection firing).
type tokenErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// idTokenClaims is the subset of the id_token JWT's own claims this
// package reads (§29.2: "an id_token whose chatgpt_account_id claim
// becomes the accountId"). Parsed WITHOUT signature verification --
// deliberate, not an oversight: this token was just received directly
// from auth.openai.com over TLS, in the SAME response as the access/
// refresh tokens this package already trusts unconditionally, so there is
// no untrusted-transport step signature verification would guard against
// here (unlike e.g. validating a bearer token presented BY a caller).
type idTokenClaims struct {
	ChatGPTAccountID string `json:"chatgpt_account_id"`
}
