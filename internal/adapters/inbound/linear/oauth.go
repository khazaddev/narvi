package linear

import (
	"golang.org/x/oauth2"

	"github.com/narvidev/narvi/internal/platform"
)

// authorizeURL and tokenURL are Linear's own real OAuth2 endpoints --
// verified live against Linear's own developer documentation during this
// Step's investigation ("Authorization Endpoint: https://linear.app/oauth/
// authorize", "Token Exchange Endpoint: https://api.linear.app/oauth/
// token"). golang.org/x/oauth2 has no built-in Linear endpoint (unlike
// its github.Endpoint, already used by internal/adapters/inbound/auth's
// own NewGitHubOAuthConfig), so these are constructed directly here --
// the ONLY place either literal appears in this package.
const (
	authorizeURL = "https://linear.app/oauth/authorize"
	tokenURL     = "https://api.linear.app/oauth/token"
)

// installCallbackPath is the fixed path NewOAuthConfig appends to
// cfg.PublicBaseURL to build the OAuth RedirectURL -- mirrors
// internal/adapters/inbound/auth's own oauthCallbackPath precedent
// exactly, so this constructed URL and cmd/control-plane/main.go's own
// route registration (GET /auth/linear/callback) can never drift apart.
const installCallbackPath = "/auth/linear/callback"

// installScopes are the scopes this Step's own workspace-install flow
// requests -- verified against Linear's real docs during this Step's
// investigation:
//
//   - read/write -- the standard base scopes every OAuth2 app needs to
//     call Linear's API at all.
//   - app:mentionable/app:assignable -- Linear's own AGENT-specific
//     scopes ("Allow the app to be mentioned in issues..."/"Allow the app
//     to be assigned as a delegate on issues...") -- without these, this
//     Step's whole AgentSessionEvent webhook flow would never fire at
//     all, since a session is only created when the app is mentioned or
//     delegated an issue in the first place.
//
// Deliberately NOT admin: Linear's own docs are explicit that "integrations
// using the actor=app mode are not able to also request admin scope" --
// requesting it would make the whole authorization request fail.
var installScopes = []string{"read", "write", "app:mentionable", "app:assignable"}

// actorAppParam is the authorization-url query parameter that switches
// Linear's standard OAuth2 flow from "authenticate as the installing
// human user" to "install this app at WORKSPACE scope" -- Linear's own
// real docs, verified live during this Step's investigation: "add the
// actor=app parameter to switch to an app installation rather than
// requesting authentication as the installing user... because this will
// be installed with a workspace scope, admin permissions are required to
// complete the installation." This is the crux of this Step's own OAuth
// scoping decision: it is what makes this flow a workspace CONNECTION,
// never a second way for a human to log into Narvi itself (see doc.go).
const actorAppParam = "actor"
const actorAppValue = "app"

// NewOAuthConfig builds the golang.org/x/oauth2.Config for Linear's own
// workspace-install OAuth2 flow -- structurally mirrors internal/
// adapters/inbound/auth's own NewGitHubOAuthConfig (same library, same
// shape), but is a functionally distinct flow: see this package's own
// doc.go for why. cfg.LinearOAuthClientID/LinearOAuthClientSecret are
// §8.10's own, SEPARATE credentials from cfg.GitHubClientID/
// GitHubClientSecret (see internal/platform/config.go's own doc comment
// on those env vars for the full reasoning).
func NewOAuthConfig(cfg platform.Config) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     cfg.LinearOAuthClientID,
		ClientSecret: cfg.LinearOAuthClientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:  authorizeURL,
			TokenURL: tokenURL,
		},
		RedirectURL: cfg.PublicBaseURL + installCallbackPath,
		Scopes:      installScopes,
	}
}
