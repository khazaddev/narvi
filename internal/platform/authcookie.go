// This file (authcookie.go) implements the Set-Cookie construction for
// §13.1's ("auth v1") backend-issued user session (§13.1: "Sessions:
// backend-issued, host-scoped cookies (HttpOnly, SameSite=Lax; never a
// default cookie name on a shared parent domain — a colliding cookie from
// a sibling app on the parent domain is a classic random-logout cause)").
// The actual verify/mint/revoke logic lives in internal/adapters/inbound/
// auth (Middleware, NewCallbackHandler, NewLogoutHandler); this file only
// builds the *http.Cookie values those call sites set, so the exact
// attributes (HttpOnly/SameSite/Path/absence-of-Domain/Secure-per-stage)
// are defined in exactly one place.

package platform

import (
	"net/http"
	"time"
)

// AuthSessionCookieName is the cookie carrying the plaintext user-session
// bearer token (hashed at rest via HashToken before being persisted — see
// tokenhash.go — never stored in plaintext anywhere). A specific,
// narvi-prefixed name, not a generic "session"/"sid" likely to collide
// with a sibling app on a shared parent domain — §13.1's own explicit
// warning.
const AuthSessionCookieName = "narvi_auth_session"

// WithAuthSessionCookie builds the Set-Cookie value for a freshly minted
// user session, carrying token (the PLAINTEXT bearer value — only
// HashToken(token) is ever persisted) and expiring at expiresAt:
//
//   - HttpOnly: never readable from JS (§13.1).
//   - SameSite=Lax: §13.1, explicit.
//   - Path=/: valid for the whole app.
//   - NO Domain field set at all: omitting Domain makes the cookie
//     host-only by default in every browser — this IS "host-scoped" per
//     §13.1; setting an explicit Domain (even one matching PublicBaseURL's
//     own host) would be redundant at best and a real footgun if ever
//     misconfigured wider than intended.
//   - Secure: true unless secure is explicitly false — callers pass
//     cfg.Stage != StageDevelopment (a cookie marked Secure is simply never
//     sent over plain http://, which is exactly what a local dev loop
//     needs relaxed; everywhere else it must always be true).
func WithAuthSessionCookie(token string, expiresAt time.Time, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     AuthSessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// ExpiredAuthSessionCookie builds a Set-Cookie value that clears the
// browser's own narvi_auth_session cookie: empty value, MaxAge<0 (the
// standard "delete this cookie now" idiom — Go's net/http translates a
// negative MaxAge into "Max-Age=0" on the wire, which every browser treats
// as "expire immediately"). Used by logout (internal/adapters/inbound/
// auth's own NewLogoutHandler) regardless of whether a matching
// server-side row was actually found — the browser's cookie is always
// cleared. Every other attribute matches WithAuthSessionCookie exactly, so
// the browser recognizes this as clearing the SAME cookie it set.
func ExpiredAuthSessionCookie(secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     AuthSessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
}
