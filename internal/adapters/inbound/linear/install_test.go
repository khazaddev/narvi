package linear_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/narvidev/narvi/internal/adapters/inbound/linear"
	"github.com/narvidev/narvi/internal/platform"
)

// TestNewInstallHandler_RedirectsWithActorAppAndSetsStateCookie proves
// GET /auth/linear/install, requested by an admin actor, mints a state
// cookie and redirects (302) to Linear's own real authorization URL,
// carrying BOTH a state query parameter matching the cookie's own value
// AND actor=app (the parameter that switches Linear's standard OAuth2
// flow into a workspace-scope app installation -- see oauth.go's own doc
// comment on actorAppParam).
func TestNewInstallHandler_RedirectsWithActorAppAndSetsStateCookie(t *testing.T) {
	cfg := platform.Config{
		LinearOAuthClientID:     "test-linear-client-id",
		LinearOAuthClientSecret: "test-linear-client-secret",
		PublicBaseURL:           "https://narvi.example.com",
	}
	oauthConfig := linear.NewOAuthConfig(cfg)

	handler := linear.NewInstallHandler(oauthConfig, platform.DefaultTimeouts(), true)

	req := httptest.NewRequest(http.MethodGet, "/auth/linear/install", nil)
	req = req.WithContext(platform.WithUser(req.Context(), platform.AuthenticatedUser{
		ID: "11111111-1111-1111-1111-111111111111", Role: "admin", Email: "admin@example.com",
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusFound, rec.Body.String())
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

// TestNewInstallHandler_NonAdmin_Forbidden proves GET /auth/linear/install
// is rejected (403), mints NO state cookie, and issues NO redirect at all
// for a signed-in Narvi user who is not an admin -- the audit finding this
// package's own authz.go fixes: previously any signed-in user (viewer
// included) could reach Linear's own authorization URL and later complete
// a workspace connection, despite domain/authz.ActionManageIntegrations
// already being admin-only per docs/TECHNICAL_PLAN.md §13.3's matrix.
func TestNewInstallHandler_NonAdmin_Forbidden(t *testing.T) {
	for _, role := range []string{"viewer", "member", "maintainer"} {
		t.Run(role, func(t *testing.T) {
			cfg := platform.Config{
				LinearOAuthClientID:     "test-linear-client-id",
				LinearOAuthClientSecret: "test-linear-client-secret",
				PublicBaseURL:           "https://narvi.example.com",
			}
			oauthConfig := linear.NewOAuthConfig(cfg)
			handler := linear.NewInstallHandler(oauthConfig, platform.DefaultTimeouts(), true)

			req := httptest.NewRequest(http.MethodGet, "/auth/linear/install", nil)
			req = req.WithContext(platform.WithUser(req.Context(), platform.AuthenticatedUser{
				ID: "22222222-2222-2222-2222-222222222222", Role: role, Email: "notadmin@example.com",
			}))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusForbidden, rec.Body.String())
			}
			if loc := rec.Header().Get("Location"); loc != "" {
				t.Errorf("Location = %q, want no redirect at all", loc)
			}
			for _, c := range rec.Result().Cookies() {
				if c.Name == "narvi_linear_install_state" {
					t.Error("narvi_linear_install_state cookie was set despite the actor being rejected")
				}
			}
		})
	}
}

// TestNewInstallHandler_NoAuthenticatedUser_InternalError proves a missing
// context user (the route not actually mounted behind auth.Middleware, or
// a test that forgot to set one) fails closed with 500, never proceeding
// as if the actor were permitted -- mirrors internal/adapters/inbound/
// httpapi's own authorize()/authenticatedUserID identical "unreachable in
// production, defended against anyway" precedent.
func TestNewInstallHandler_NoAuthenticatedUser_InternalError(t *testing.T) {
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

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
}
