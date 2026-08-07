package chatgptoauth

import "fmt"

// TokenError is a failed POST /oauth/token call, carrying the standard
// OAuth2 error code (RFC 6749 §5.2) when the response body parsed as one.
type TokenError struct {
	StatusCode  int
	Code        string // e.g. "invalid_grant", "refresh_token_reused" -- may be empty if the body didn't parse as the standard error shape.
	Description string
}

func (e *TokenError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("chatgptoauth: oauth/token failed: %s (%s): %s", e.Code, httpStatusText(e.StatusCode), e.Description)
	}
	return fmt.Sprintf("chatgptoauth: oauth/token failed: http %s", httpStatusText(e.StatusCode))
}

// IsTerminal reports whether e represents a terminal failure the refresh
// pump (internal/app/chatgptrefresh, §29.5) must stop retrying and instead
// mark oauth_needs_relink -- §29.2's own two named terminal codes:
// invalid_grant (a bad/expired/revoked grant) and refresh_token_reused
// (OpenAI's own reuse-detection firing). Mirrors §13.2's own rule ("a
// provider email-API failure is a retryable error, not an empty identity"
// -- default to retryable): any OTHER code, or an unparseable body (empty
// Code), is conservatively treated as transient, never terminal, so an
// unrecognized failure mode degrades to "retry next pump cycle" rather
// than prematurely forcing a user through re-linking.
func (e *TokenError) IsTerminal() bool {
	switch e.Code {
	case "invalid_grant", "refresh_token_reused":
		return true
	default:
		return false
	}
}

func httpStatusText(code int) string {
	if code == 0 {
		return "no response"
	}
	return fmt.Sprintf("status %d", code)
}
