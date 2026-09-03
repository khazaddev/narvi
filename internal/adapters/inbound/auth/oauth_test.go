package auth_test

import (
	"slices"
	"testing"

	"github.com/narvidev/narvi/internal/adapters/inbound/auth"
	"github.com/narvidev/narvi/internal/platform"
)

// TestNewGitHubOAuthConfig proves the built oauth2.Config threads
// ClientID/ClientSecret through unchanged, points at GitHub's own real
// endpoint (golang.org/x/oauth2/github.Endpoint), builds RedirectURL as
// PublicBaseURL + "/auth/github/callback", and requests exactly the 3
// scopes this Step's own design decision names (user:email, read:org,
// repo — see oauth.go's own doc comment on why repo is requested despite
// being unused by this Step).
func TestNewGitHubOAuthConfig(t *testing.T) {
	cfg := platform.Config{
		GitHubClientID:     "test-id",
		GitHubClientSecret: "test-secret",
		PublicBaseURL:      "https://narvi.example.com",
	}

	got := auth.NewGitHubOAuthConfig(cfg)

	if got.ClientID != "test-id" {
		t.Errorf("ClientID = %q, want %q", got.ClientID, "test-id")
	}
	if got.ClientSecret != "test-secret" {
		t.Errorf("ClientSecret = %q, want %q", got.ClientSecret, "test-secret")
	}
	if got.RedirectURL != "https://narvi.example.com/auth/github/callback" {
		t.Errorf("RedirectURL = %q, want %q", got.RedirectURL, "https://narvi.example.com/auth/github/callback")
	}
	if got.Endpoint.AuthURL == "" || got.Endpoint.TokenURL == "" {
		t.Error("Endpoint AuthURL/TokenURL are empty, want GitHub's own real endpoint URLs")
	}
	wantScopes := []string{"user:email", "read:org", "repo"}
	if !slices.Equal(got.Scopes, wantScopes) {
		t.Errorf("Scopes = %v, want %v", got.Scopes, wantScopes)
	}
}
