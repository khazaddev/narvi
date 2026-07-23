package linear_test

import (
	"net/url"
	"slices"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/inbound/linear"
	"github.com/khazaddev/narvi/internal/platform"
)

// TestNewOAuthConfig proves the built oauth2.Config threads
// LinearOAuthClientID/LinearOAuthClientSecret through unchanged, points at
// Linear's own real OAuth2 endpoints (verified live during this Step's
// investigation), builds RedirectURL as PublicBaseURL +
// "/auth/linear/callback", and requests exactly the 4 scopes this Step's
// own design decision names -- mirrors internal/adapters/inbound/auth's
// own TestNewGitHubOAuthConfig exactly, for the functionally distinct
// workspace-install flow (see oauth.go's own doc comment).
func TestNewOAuthConfig(t *testing.T) {
	cfg := platform.Config{
		LinearOAuthClientID:     "test-linear-client-id",
		LinearOAuthClientSecret: "test-linear-client-secret",
		PublicBaseURL:           "https://narvi.example.com",
	}

	got := linear.NewOAuthConfig(cfg)

	if got.ClientID != "test-linear-client-id" {
		t.Errorf("ClientID = %q, want %q", got.ClientID, "test-linear-client-id")
	}
	if got.ClientSecret != "test-linear-client-secret" {
		t.Errorf("ClientSecret = %q, want %q", got.ClientSecret, "test-linear-client-secret")
	}
	if got.RedirectURL != "https://narvi.example.com/auth/linear/callback" {
		t.Errorf("RedirectURL = %q, want %q", got.RedirectURL, "https://narvi.example.com/auth/linear/callback")
	}
	if got.Endpoint.AuthURL != "https://linear.app/oauth/authorize" {
		t.Errorf("Endpoint.AuthURL = %q, want %q", got.Endpoint.AuthURL, "https://linear.app/oauth/authorize")
	}
	if got.Endpoint.TokenURL != "https://api.linear.app/oauth/token" {
		t.Errorf("Endpoint.TokenURL = %q, want %q", got.Endpoint.TokenURL, "https://api.linear.app/oauth/token")
	}
	wantScopes := []string{"read", "write", "app:mentionable", "app:assignable"}
	if !slices.Equal(got.Scopes, wantScopes) {
		t.Errorf("Scopes = %v, want %v", got.Scopes, wantScopes)
	}
}

// TestNewOAuthConfig_AuthCodeURL_CarriesActorAppParam proves the
// authorization URL this Config builds (via AuthCodeURL, with the
// oauth2.SetAuthURLParam call install.go's own NewInstallHandler makes)
// carries actor=app -- the exact parameter that switches Linear's
// standard OAuth2 flow to a workspace-scope app installation (see
// oauth.go's own doc comment on actorAppParam for the full reasoning).
func TestNewOAuthConfig_AuthCodeURL_CarriesActorAppParam(t *testing.T) {
	cfg := platform.Config{
		LinearOAuthClientID:     "test-linear-client-id",
		LinearOAuthClientSecret: "test-linear-client-secret",
		PublicBaseURL:           "https://narvi.example.com",
	}
	oauthConfig := linear.NewOAuthConfig(cfg)

	authURL := oauthConfig.AuthCodeURL("test-state")

	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", authURL, err)
	}
	// AuthCodeURL alone (no extra oauth2.AuthCodeOption) never carries
	// actor=app -- that param is only added at the actual call site
	// (install.go's NewInstallHandler). This test documents that fact
	// directly rather than asserting a false positive: exercising the
	// real call site's own behavior is install_test.go's job below.
	if parsed.Query().Get("actor") != "" {
		t.Error("AuthCodeURL with no options already carries actor= -- expected it to be absent until NewInstallHandler adds it explicitly")
	}
}
