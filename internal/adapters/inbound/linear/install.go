package linear

import (
	"net/http"
	"time"

	"golang.org/x/oauth2"

	"github.com/khazaddev/narvi/internal/platform"
)

// installStateCookieName is the SEPARATE, short-lived cookie carrying the
// CSRF-protection state value for this flow -- a DIFFERENT cookie from
// internal/adapters/inbound/auth's own oauthStateCookieName (that one
// guards the GitHub-login flow; the two must never collide or be
// confused, since a stale state cookie from one flow must never be
// accepted by the other's callback).
const installStateCookieName = "narvi_linear_install_state"

// NewInstallHandler backs GET /auth/linear/install: mints a fresh random
// state value, stores it in a short-lived cookie, and redirects (302) to
// Linear's own authorization URL with the `actor=app` parameter (see
// oauth.go's own doc comment on actorAppParam) -- structurally mirrors
// internal/adapters/inbound/auth's own NewLoginHandler exactly (same
// plain state-parameter CSRF protection, deliberately NOT PKCE, for the
// same reason: this is a confidential, server-side client holding
// LinearOAuthClientSecret securely).
//
// Mounted behind auth.Middleware (a Narvi user must already be signed in
// to initiate a workspace connection) AND, as of this fix, behind
// requireManageIntegrations (authz.go): only an admin actor may mint a
// state cookie and be redirected into Linear's own authorization flow at
// all -- see authz.go's own doc comment for why this is admin-only.
func NewInstallHandler(oauthConfig *oauth2.Config, timeouts platform.Timeouts, secureCookies bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger := platform.Logger(r.Context())

		if _, ok := requireManageIntegrations(w, r); !ok {
			return
		}

		state, err := platform.GenerateToken()
		if err != nil {
			logger.Error("linear: generate oauth state failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:     installStateCookieName,
			Value:    state,
			Path:     "/",
			Expires:  time.Now().Add(timeouts.OAuthStateTTL),
			HttpOnly: true,
			Secure:   secureCookies,
			SameSite: http.SameSiteLaxMode,
		})

		authURL := oauthConfig.AuthCodeURL(state, oauth2.SetAuthURLParam(actorAppParam, actorAppValue))
		http.Redirect(w, r, authURL, http.StatusFound)
	}
}

// expiredCookie builds a Set-Cookie value that clears name immediately --
// mirrors internal/adapters/inbound/auth's own identical helper (a
// package-private copy, not shared, matching that package's own
// unexported-per-package convention).
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
