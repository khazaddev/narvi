package auth

import (
	"net/http"
	"time"

	"golang.org/x/oauth2"

	"github.com/khazaddev/narvi/internal/platform"
)

// oauthStateCookieName is the SEPARATE, short-lived cookie carrying the
// CSRF-protection state value -- distinct from platform.
// AuthSessionCookieName (the actual signed-in session, minted only after a
// successful callback): this cookie never survives past a single login
// attempt, and is cleared by NewCallbackHandler as soon as it is read.
const oauthStateCookieName = "narvi_oauth_state"

// oauthNextCookieName is Step 39's ("identities + full RBAC", §13.2) own
// addition -- a SEPARATE short-lived cookie carrying an optional
// post-login redirect target, so a caller that sends a signed-out visitor
// through this SAME GitHub OAuth login flow (internal/adapters/inbound/
// identitylink's magic-link consume handler, when the clicking visitor
// isn't signed in yet) lands back where they actually meant to go, rather
// than NewCallbackHandler's own fixed "/" default. Mirrors
// oauthStateCookieName's own identical lifecycle (minted here, read and
// cleared exactly once by NewCallbackHandler, never replayed) -- kept as
// its OWN cookie rather than folded into the state value itself, so the
// state cookie's own CSRF-protection shape (an opaque, unguessable token,
// exact-string-compared) never has to also double as a data-carrying
// value.
const oauthNextCookieName = "narvi_oauth_next"

// isSafeRedirectNext reports whether next is safe to redirect a visitor to
// after login -- ONLY a same-origin, absolute-path redirect (starts with
// exactly one "/", never "//..." or "/\\..." -- both of which browsers can
// interpret as a scheme-relative URL to a DIFFERENT origin, the classic
// open-redirect vector) is accepted; anything else (empty, a full
// "https://..." URL, a scheme-relative "//evil.example.com" URL, a
// backslash-prefixed variant) is rejected. Called by NewLoginHandler
// before ever storing next in a cookie, and defensively re-checked by
// NewCallbackHandler before ever using the cookie's own stored value as a
// redirect target -- belt and suspenders, since an attacker who could
// somehow forge or tamper with the cookie value directly (bypassing this
// function's own first call in NewLoginHandler) must still not be able to
// redirect a victim off-site.
func isSafeRedirectNext(next string) bool {
	if next == "" || next[0] != '/' {
		return false
	}
	if len(next) > 1 && (next[1] == '/' || next[1] == '\\') {
		return false
	}
	return true
}

// NewLoginHandler backs GET /auth/github/login (§13.1): mints a fresh
// random state value (platform.GenerateToken is a fine reuse here too --
// it's just "a fresh unguessable random string", not specifically a bearer
// credential this time), stores it in a short-lived, HttpOnly, host-scoped
// cookie (expiring after timeouts.OAuthStateTTL), and redirects (302) to
// oauthConfig.AuthCodeURL(state).
//
// Plain state-parameter CSRF protection, deliberately NOT PKCE: PKCE
// exists for PUBLIC clients that cannot hold a client secret (mobile/SPA);
// this is a confidential, server-side client holding GitHubClientSecret
// securely, so plain state-parameter protection is the correct, standard
// choice here and keeps the flow simpler.
//
// Step 39 ("identities + full RBAC", §13.2) update: an OPTIONAL ?next=
// query parameter (a same-origin absolute path ONLY -- isSafeRedirectNext,
// this file's own doc comment) is stored in a second short-lived cookie
// (oauthNextCookieName) and honored by NewCallbackHandler as the final
// post-login redirect target, instead of that handler's own fixed "/"
// default -- lets internal/adapters/inbound/identitylink's magic-link
// consume handler send a signed-out visitor through this SAME login flow
// and land back on the magic-link URL afterward. Absent or unsafe next is
// silently ignored (no cookie set at all) -- this endpoint's own existing
// behavior (redirect to "/" after login) is completely unchanged for
// every caller that doesn't pass one.
func NewLoginHandler(oauthConfig *oauth2.Config, timeouts platform.Timeouts, secureCookies bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger := platform.Logger(r.Context())

		state, err := platform.GenerateToken()
		if err != nil {
			logger.Error("auth: generate oauth state failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:     oauthStateCookieName,
			Value:    state,
			Path:     "/",
			Expires:  time.Now().Add(timeouts.OAuthStateTTL),
			HttpOnly: true,
			Secure:   secureCookies,
			SameSite: http.SameSiteLaxMode,
		})

		if next := r.URL.Query().Get("next"); isSafeRedirectNext(next) {
			http.SetCookie(w, &http.Cookie{
				Name:     oauthNextCookieName,
				Value:    next,
				Path:     "/",
				Expires:  time.Now().Add(timeouts.OAuthStateTTL),
				HttpOnly: true,
				Secure:   secureCookies,
				SameSite: http.SameSiteLaxMode,
			})
		}

		http.Redirect(w, r, oauthConfig.AuthCodeURL(state), http.StatusFound)
	}
}

// expiredCookie builds a Set-Cookie value that clears name immediately --
// used to consume the oauth-state cookie once the callback has read it (so
// it can never be replayed), matching platform.ExpiredAuthSessionCookie's
// own MaxAge<0 idiom for the SEPARATE, longer-lived session cookie.
func expiredCookie(name string, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
}
