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
