//go:build integration

// Integration test for the Linear workspace-install OAuth callback (Step
// 34, "Linear ingress", §8.10's own "OAuth" scope) -- mirrors
// internal/adapters/inbound/auth's own auth_integration_test.go style
// (design decision 12: fake the provider's own token endpoint via a local
// httptest.Server, exactly like that package's own fakeTokenServer),
// against a real Postgres instance. Uses this package's own newTestPool
// (webhook_integration_test.go, same linear_test package).
package linear_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/khazaddev/narvi/internal/adapters/inbound/linear"
	"github.com/khazaddev/narvi/internal/adapters/outbound/linearapi"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/platform"
)

// fakeLinearOAuthServer stands in for BOTH of Linear's own real hosts this
// flow talks to: the OAuth token endpoint (POST /oauth/token) and the
// GraphQL API (POST /graphql, this Step's own ViewerAndOrganization
// query) -- one httptest.Server serving both paths, since linearapi.
// Client and oauth2.Config each take their own independent base URL and
// both can point at the same fake host.
func fakeLinearOAuthServer(t *testing.T, wantCode, accessToken, appUserID, organizationID string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse token request form: %v", err)
		}
		if got := r.PostForm.Get("code"); got != wantCode {
			t.Errorf("token request code = %q, want %q", got, wantCode)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  accessToken,
			"token_type":    "Bearer",
			"expires_in":    86399,
			"refresh_token": "test-refresh-token",
		})
	})
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+accessToken {
			t.Errorf("graphql request Authorization = %q, want %q", auth, "Bearer "+accessToken)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"viewer":       map[string]any{"id": appUserID},
				"organization": map[string]any{"id": organizationID},
			},
		})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// TestInstallCallback_ValidExchange_StoresInstallation proves the full
// callback flow: state check passes, the code is exchanged at the fake
// token endpoint, ViewerAndOrganization is fetched from the fake GraphQL
// endpoint, and a linear_installations row is stored with the org's own
// id, the app-user id, and both tokens encrypted at rest (never the
// plaintext value).
func TestInstallCallback_ValidExchange_StoresInstallation(t *testing.T) {
	pool := newTestPool(t)
	installations := narvipg.NewLinearInstallationStore(pool)
	tokenEncryptionKey := []byte("01234567890123456789012345678901") // exactly 32 bytes

	const (
		wantCode       = "test-authorization-code"
		accessToken    = "test-linear-access-token"
		appUserID      = "app-user-42"
		organizationID = "org-installed-42"
	)
	server := fakeLinearOAuthServer(t, wantCode, accessToken, appUserID, organizationID)

	oauthConfig := &oauth2.Config{
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Endpoint: oauth2.Endpoint{
			AuthURL:  server.URL + "/oauth/authorize",
			TokenURL: server.URL + "/oauth/token",
		},
	}
	linearClient := linearapi.New(server.Client(), server.URL)

	handler := linear.NewInstallCallbackHandler(oauthConfig, linearClient, installations, tokenEncryptionKey, false)

	req := httptest.NewRequest(http.MethodGet, "/auth/linear/callback?"+url.Values{
		"code":  {wantCode},
		"state": {"test-state-value"},
	}.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: "narvi_linear_install_state", Value: "test-state-value"})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	got, err := installations.GetByOrganizationID(context.Background(), organizationID)
	if err != nil {
		t.Fatalf("GetByOrganizationID: %v", err)
	}
	if got.AppUserID != appUserID {
		t.Errorf("AppUserID = %q, want %q", got.AppUserID, appUserID)
	}
	if got.ExpiresAt.Time.Before(time.Now()) {
		t.Error("ExpiresAt is in the past, want a future expiry from the fake token response's own expires_in")
	}

	decryptedAccess, err := platform.DecryptToken(tokenEncryptionKey, got.AccessTokenEncrypted)
	if err != nil {
		t.Fatalf("DecryptToken(access): %v", err)
	}
	if string(decryptedAccess) != accessToken {
		t.Errorf("decrypted access token = %q, want %q", decryptedAccess, accessToken)
	}
	if string(got.AccessTokenEncrypted) == accessToken {
		t.Error("AccessTokenEncrypted equals the plaintext token verbatim -- it must be encrypted, not stored raw")
	}

	decryptedRefresh, err := platform.DecryptToken(tokenEncryptionKey, got.RefreshTokenEncrypted)
	if err != nil {
		t.Fatalf("DecryptToken(refresh): %v", err)
	}
	if string(decryptedRefresh) != "test-refresh-token" {
		t.Errorf("decrypted refresh token = %q, want %q", decryptedRefresh, "test-refresh-token")
	}
}

// TestInstallCallback_StateMismatch_Rejected proves a missing/mismatched
// state cookie is rejected BEFORE any token exchange is attempted -- the
// fake server's own /oauth/token handler asserts it is never called by
// failing the test if it is (via wantCode's own strict check), so a bug
// that skipped the state check would surface here as a test failure on
// that assertion, not just the wrong status code.
func TestInstallCallback_StateMismatch_Rejected(t *testing.T) {
	pool := newTestPool(t)
	installations := narvipg.NewLinearInstallationStore(pool)

	tokenEndpointCalled := false
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, _ *http.Request) {
		tokenEndpointCalled = true
		w.WriteHeader(http.StatusOK)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	oauthConfig := &oauth2.Config{
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Endpoint: oauth2.Endpoint{
			AuthURL:  server.URL + "/oauth/authorize",
			TokenURL: server.URL + "/oauth/token",
		},
	}
	linearClient := linearapi.New(server.Client(), server.URL)
	tokenEncryptionKey := []byte("01234567890123456789012345678901")

	handler := linear.NewInstallCallbackHandler(oauthConfig, linearClient, installations, tokenEncryptionKey, false)

	req := httptest.NewRequest(http.MethodGet, "/auth/linear/callback?code=whatever&state=state-from-query", nil)
	req.AddCookie(&http.Cookie{Name: "narvi_linear_install_state", Value: "state-from-cookie-DOES-NOT-MATCH"})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if tokenEndpointCalled {
		t.Error("token endpoint was called despite a state mismatch -- the exchange must never be attempted")
	}
}
