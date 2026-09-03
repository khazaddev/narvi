//go:build integration

// Integration test for the Linear workspace-install OAuth callback
// ("Linear ingress", §8.10's own "OAuth" scope) -- mirrors
// internal/adapters/inbound/auth's own auth_integration_test.go style
// (design decision 12: fake the provider's own token endpoint via a local
// httptest.Server, exactly like that package's own fakeTokenServer),
// against a real Postgres instance. Uses this package's own newTestPool
// (webhook_integration_test.go, same linear_test package).
//
// Also covers the audit-fix batch that added authz.go's own
// requireManageIntegrations gate ahead of both this handler and
// NewInstallHandler, and the audit_log row this handler's own transaction
// now writes alongside the installations Upsert (see callback.go's own
// updated doc comment).
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

	"github.com/narvidev/narvi/internal/adapters/inbound/linear"
	"github.com/narvidev/narvi/internal/adapters/outbound/linearapi"
	narvipg "github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/platform"
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

// createTestUserWithRole inserts a fixture user with the given role --
// shared by every test below that needs a real users.id to satisfy
// linear_installations.connected_by_user_id/audit_log.actor_user_id's own
// FK constraints (both reference users(id)).
func createTestUserWithRole(ctx context.Context, t *testing.T, users *narvipg.UserStore, email string, role sqlcgen.UserRole) sqlcgen.User {
	t.Helper()
	user, err := users.Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: email, DisplayName: email, Role: role,
	})
	if err != nil {
		t.Fatalf("create fixture user (role %s): %v", role, err)
	}
	return user
}

// withActor returns req carrying an AuthenticatedUser context value for
// user -- stands in for auth.Middleware's own real session-cookie
// resolution (both /auth/linear/install and /auth/linear/callback are
// mounted behind it in production; these HTTP-level tests call the
// handler directly, so the context value is supplied here instead).
func withActor(req *http.Request, user sqlcgen.User) *http.Request {
	return req.WithContext(platform.WithUser(req.Context(), platform.AuthenticatedUser{
		ID: user.ID.String(), Role: string(user.Role), Email: user.PrimaryEmail,
	}))
}

// TestInstallCallback_ValidExchange_StoresInstallation proves the full
// callback flow for an admin actor: state check passes, the code is
// exchanged at the fake token endpoint, ViewerAndOrganization is fetched
// from the fake GraphQL endpoint, a linear_installations row is stored
// with the org's own id, the app-user id, and both tokens encrypted at
// rest (never the plaintext value), and an audit_log row is written in
// the same transaction recording who connected which organization.
func TestInstallCallback_ValidExchange_StoresInstallation(t *testing.T) {
	pool := newTestPool(t)
	installations := narvipg.NewLinearInstallationStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)
	users := narvipg.NewUserStore(pool)
	tokenEncryptionKey := []byte("01234567890123456789012345678901") // exactly 32 bytes

	ctx := context.Background()
	admin := createTestUserWithRole(ctx, t, users, "admin-connects@example.com", sqlcgen.UserRoleAdmin)

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

	handler := linear.NewInstallCallbackHandler(oauthConfig, linearClient, pool, installations, auditLog, tokenEncryptionKey, false)

	req := httptest.NewRequest(http.MethodGet, "/auth/linear/callback?"+url.Values{
		"code":  {wantCode},
		"state": {"test-state-value"},
	}.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: "narvi_linear_install_state", Value: "test-state-value"})
	req = withActor(req, admin)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	got, err := installations.GetByOrganizationID(ctx, organizationID)
	if err != nil {
		t.Fatalf("GetByOrganizationID: %v", err)
	}
	if got.AppUserID != appUserID {
		t.Errorf("AppUserID = %q, want %q", got.AppUserID, appUserID)
	}
	if got.ExpiresAt.Time.Before(time.Now()) {
		t.Error("ExpiresAt is in the past, want a future expiry from the fake token response's own expires_in")
	}
	if got.ConnectedByUserID.Bytes != admin.ID.Bytes {
		t.Errorf("ConnectedByUserID = %v, want the acting admin's own id %v", got.ConnectedByUserID, admin.ID)
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

	// The audit finding's own second gap: an audit_log row must exist,
	// in the same transaction as the installation write, attributing the
	// connection to the admin who performed it.
	var (
		actorUserID  []byte
		action       string
		resourceType string
		resourceID   string
		detailJSON   []byte
	)
	row := pool.QueryRow(ctx, `SELECT actor_user_id, action, resource_type, resource_id, detail_json
		FROM audit_log WHERE resource_type = 'linear_installation' AND resource_id = $1`, organizationID)
	if err := row.Scan(&actorUserID, &action, &resourceType, &resourceID, &detailJSON); err != nil {
		t.Fatalf("query audit_log row: %v", err)
	}
	if action != "integration.linear_connected" {
		t.Errorf("audit_log.action = %q, want %q (first connect)", action, "integration.linear_connected")
	}
	if resourceID != organizationID {
		t.Errorf("audit_log.resource_id = %q, want %q", resourceID, organizationID)
	}
	var detail map[string]any
	if err := json.Unmarshal(detailJSON, &detail); err != nil {
		t.Fatalf("unmarshal detail_json: %v", err)
	}
	if detail["app_user_id"] == "" || detail["app_user_id"] == nil {
		t.Errorf("audit_log.detail_json[app_user_id] = %v, want a non-empty value", detail["app_user_id"])
	}
}

// TestInstallCallback_Reconnect_RecordsReconnectedAction proves a SECOND
// callback for the SAME organization_id (re-authorizing/rotating tokens
// for an already-connected workspace) is recorded with a distinct audit
// action ("integration.linear_reconnected") from a first-time connect,
// while still replacing the stored token pair in place (migrations/
// 000031_linear_installations.up.sql's own "never a history of past
// ones" invariant).
func TestInstallCallback_Reconnect_RecordsReconnectedAction(t *testing.T) {
	pool := newTestPool(t)
	installations := narvipg.NewLinearInstallationStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)
	users := narvipg.NewUserStore(pool)
	tokenEncryptionKey := []byte("01234567890123456789012345678901")

	ctx := context.Background()
	admin := createTestUserWithRole(ctx, t, users, "admin-reconnects@example.com", sqlcgen.UserRoleAdmin)

	const (
		appUserID      = "app-user-77"
		organizationID = "org-reconnect-77"
	)

	doCallback := func(code, accessToken string) *httptest.ResponseRecorder {
		server := fakeLinearOAuthServer(t, code, accessToken, appUserID, organizationID)
		oauthConfig := &oauth2.Config{
			ClientID:     "test-client-id",
			ClientSecret: "test-client-secret",
			Endpoint: oauth2.Endpoint{
				AuthURL:  server.URL + "/oauth/authorize",
				TokenURL: server.URL + "/oauth/token",
			},
		}
		linearClient := linearapi.New(server.Client(), server.URL)
		handler := linear.NewInstallCallbackHandler(oauthConfig, linearClient, pool, installations, auditLog, tokenEncryptionKey, false)

		req := httptest.NewRequest(http.MethodGet, "/auth/linear/callback?"+url.Values{
			"code":  {code},
			"state": {"test-state-value"},
		}.Encode(), nil)
		req.AddCookie(&http.Cookie{Name: "narvi_linear_install_state", Value: "test-state-value"})
		req = withActor(req, admin)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	firstRec := doCallback("first-code", "first-access-token")
	if firstRec.Code != http.StatusOK {
		t.Fatalf("first callback status = %d, want %d; body = %s", firstRec.Code, http.StatusOK, firstRec.Body.String())
	}

	secondRec := doCallback("second-code", "second-access-token")
	if secondRec.Code != http.StatusOK {
		t.Fatalf("second callback status = %d, want %d; body = %s", secondRec.Code, http.StatusOK, secondRec.Body.String())
	}

	got, err := installations.GetByOrganizationID(ctx, organizationID)
	if err != nil {
		t.Fatalf("GetByOrganizationID: %v", err)
	}
	decryptedAccess, err := platform.DecryptToken(tokenEncryptionKey, got.AccessTokenEncrypted)
	if err != nil {
		t.Fatalf("DecryptToken(access): %v", err)
	}
	if string(decryptedAccess) != "second-access-token" {
		t.Errorf("stored access token = %q, want the SECOND callback's own token (replaced in place)", decryptedAccess)
	}

	var actions []string
	rows, err := pool.Query(ctx, `SELECT action FROM audit_log WHERE resource_type = 'linear_installation' AND resource_id = $1 ORDER BY created_at ASC`, organizationID)
	if err != nil {
		t.Fatalf("query audit_log actions: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var action string
		if err := rows.Scan(&action); err != nil {
			t.Fatalf("scan action: %v", err)
		}
		actions = append(actions, action)
	}
	if len(actions) != 2 {
		t.Fatalf("audit_log has %d rows for this organization, want 2 (one per callback); actions = %v", len(actions), actions)
	}
	if actions[0] != "integration.linear_connected" {
		t.Errorf("first audit action = %q, want %q", actions[0], "integration.linear_connected")
	}
	if actions[1] != "integration.linear_reconnected" {
		t.Errorf("second audit action = %q, want %q", actions[1], "integration.linear_reconnected")
	}
}

// TestInstallCallback_StateMismatch_Rejected proves a missing/mismatched
// state cookie is rejected BEFORE any token exchange is attempted -- the
// fake server's own /oauth/token handler asserts it is never called by
// failing the test if it is (via wantCode's own strict check), so a bug
// that skipped the state check would surface here as a test failure on
// that assertion, not just the wrong status code. The request still
// carries an admin actor: this proves the state check, not the authz
// gate (covered separately below), is what rejects this request.
func TestInstallCallback_StateMismatch_Rejected(t *testing.T) {
	pool := newTestPool(t)
	installations := narvipg.NewLinearInstallationStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)
	users := narvipg.NewUserStore(pool)

	ctx := context.Background()
	admin := createTestUserWithRole(ctx, t, users, "admin-state-mismatch@example.com", sqlcgen.UserRoleAdmin)

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

	handler := linear.NewInstallCallbackHandler(oauthConfig, linearClient, pool, installations, auditLog, tokenEncryptionKey, false)

	req := httptest.NewRequest(http.MethodGet, "/auth/linear/callback?code=whatever&state=state-from-query", nil)
	req.AddCookie(&http.Cookie{Name: "narvi_linear_install_state", Value: "state-from-cookie-DOES-NOT-MATCH"})
	req = withActor(req, admin)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if tokenEndpointCalled {
		t.Error("token endpoint was called despite a state mismatch -- the exchange must never be attempted")
	}
}

// TestInstallCallback_NonAdmin_Forbidden proves the audit finding's own
// core scenario is closed: a signed-in Narvi user who is NOT an admin
// (viewer or member) gets 403 on /auth/linear/callback, even with an
// otherwise-valid state cookie and authorization code -- the oauth
// exchange is never attempted, and no linear_installations/audit_log row
// is written at all.
func TestInstallCallback_NonAdmin_Forbidden(t *testing.T) {
	for _, role := range []sqlcgen.UserRole{sqlcgen.UserRoleViewer, sqlcgen.UserRoleMember, sqlcgen.UserRoleMaintainer} {
		t.Run(string(role), func(t *testing.T) {
			pool := newTestPool(t)
			installations := narvipg.NewLinearInstallationStore(pool)
			auditLog := narvipg.NewAuditLogStore(pool)
			users := narvipg.NewUserStore(pool)

			ctx := context.Background()
			actor := createTestUserWithRole(ctx, t, users, "nonadmin-"+string(role)+"@example.com", role)

			const (
				wantCode       = "test-authorization-code"
				accessToken    = "test-linear-access-token"
				appUserID      = "app-user-nonadmin"
				organizationID = "org-nonadmin-attempt"
			)
			tokenEndpointCalled := false
			mux := http.NewServeMux()
			mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, _ *http.Request) {
				tokenEndpointCalled = true
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token": accessToken, "token_type": "Bearer", "expires_in": 86399,
				})
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

			handler := linear.NewInstallCallbackHandler(oauthConfig, linearClient, pool, installations, auditLog, tokenEncryptionKey, false)

			req := httptest.NewRequest(http.MethodGet, "/auth/linear/callback?"+url.Values{
				"code":  {wantCode},
				"state": {"test-state-value"},
			}.Encode(), nil)
			req.AddCookie(&http.Cookie{Name: "narvi_linear_install_state", Value: "test-state-value"})
			req = withActor(req, actor)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusForbidden, rec.Body.String())
			}
			if tokenEndpointCalled {
				t.Error("token endpoint was called despite a non-admin actor -- the exchange must never be attempted")
			}

			if _, err := installations.GetByOrganizationID(ctx, organizationID); err == nil {
				t.Error("a linear_installations row was created despite a non-admin actor")
			}

			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE resource_type = 'linear_installation' AND resource_id = $1`, organizationID).Scan(&count); err != nil {
				t.Fatalf("count audit_log rows: %v", err)
			}
			if count != 0 {
				t.Errorf("audit_log has %d rows for this organization, want 0 (nothing should be recorded for a rejected actor)", count)
			}
		})
	}
}
