package linear_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/inbound/linear"
	"github.com/khazaddev/narvi/internal/platform"
)

// TestNewInstallHandler_RedirectsWithActorAppAndSetsStateCookie proves
// GET /auth/linear/install mints a state cookie and redirects (302) to
// Linear's own real authorization URL, carrying BOTH a state query
// parameter matching the cookie's own value AND actor=app (the parameter
// that switches Linear's standard OAuth2 flow into a workspace-scope app
// installation -- see oauth.go's own doc comment on actorAppParam).
func TestNewInstallHandler_RedirectsWithActorAppAndSetsStateCookie(t *testing.T) {
	cfg := platform.Config{
		LinearOAuthClientID:     "test-linear-client-id",
		LinearOAuthClientSecret: "test-linear-client-secret",
		PublicBaseURL:           "https://narvi.example.com",
	}
	oauthConfig := linear.NewOAuthConfig(cfg)

	handler := linear.NewInstallHandler(oauthConfig, platform.DefaultTimeouts(), true)

	req := httptest.NewRequest(http.MethodGet, "/auth/linear/install", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}

	location := rec.Header().Get("Location")
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", location, err)
	}
	if got := parsed.Query().Get("actor"); got != "app" {
		t.Errorf("redirect actor param = %q, want %q", got, "app")
	}
	if parsed.Query().Get("client_id") != "test-linear-client-id" {
		t.Errorf("redirect client_id = %q, want %q", parsed.Query().Get("client_id"), "test-linear-client-id")
	}

	stateParam := parsed.Query().Get("state")
	if stateParam == "" {
		t.Fatal("redirect state param is empty")
	}

	resp := rec.Result()
	var stateCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "narvi_linear_install_state" {
			stateCookie = c
		}
	}
	if stateCookie == nil {
		t.Fatal("narvi_linear_install_state cookie not set")
	}
	if stateCookie.Value != stateParam {
		t.Errorf("state cookie value = %q, want %q (must match the redirect's own state param)", stateCookie.Value, stateParam)
	}
	if !stateCookie.HttpOnly {
		t.Error("state cookie is not HttpOnly")
	}
	if !stateCookie.Secure {
		t.Error("state cookie is not Secure (secureCookies=true was passed)")
	}
}
