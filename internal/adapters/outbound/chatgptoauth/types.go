package chatgptoauth

// usercodeRequest is POST /api/accounts/deviceauth/usercode's request body
// (§29.2: "POST https://auth.openai.com/api/accounts/deviceauth/usercode
// {client_id}").
type usercodeRequest struct {
	ClientID string `json:"client_id"`
}

// usercodeResponse is that same call's response (§29.2: "{device_auth_id,
// user_code, interval}").
type usercodeResponse struct {
	DeviceAuthID string `json:"device_auth_id"`
	UserCode     string `json:"user_code"`
	// Interval is the server-provided minimum seconds between poll
	// attempts (§29.3 point 2: "throttled by the server-provided interval
	// via last_polled_at").
	Interval int `json:"interval"`
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
