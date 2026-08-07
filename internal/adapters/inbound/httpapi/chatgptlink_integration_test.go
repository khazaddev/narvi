//go:build integration

// Integration tests for /api/me/chatgpt-link (Step 59, "models: Codex via
// ChatGPT-account OAuth", §29.3/§29.9) against a real Postgres instance,
// proving the REST route wiring (auth, RBAC, request/response shape) on
// top of internal/app/chatgptlink's own already-thoroughly-tested service
// logic (internal/app/chatgptlink/service_integration_test.go covers the
// state-machine behavior itself: mint/reuse, throttled/granted polling,
// link-then-unlink -- deliberately NOT re-proven exhaustively here).
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/khazaddev/narvi/internal/adapters/outbound/chatgptoauth"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

type chatGPTLinkStatusForTest struct {
	Status          string  `json:"status"`
	VerificationURL *string `json:"verificationUrl"`
	UserCode        *string `json:"userCode"`
	ExpiresAt       *string `json:"expiresAt"`
}

func TestChatGPTLink_RequiresAuth(t *testing.T) {
	rig := newTestRig(t)

	for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		status := rig.doJSON(t, method, "/api/me/chatgpt-link", nil, nil, "" /* no auth cookie */)
		if status != http.StatusUnauthorized {
			t.Errorf("%s /api/me/chatgpt-link with no auth cookie: status = %d, want %d", method, status, http.StatusUnauthorized)
		}
	}
}

func TestChatGPTLink_ViewerForbidden(t *testing.T) {
	ctx := context.Background()
	rig := newTestRig(t)
	_, viewerToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleViewer)

	for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		status := rig.doJSON(t, method, "/api/me/chatgpt-link", nil, nil, viewerToken)
		if status != http.StatusForbidden {
			t.Errorf("%s /api/me/chatgpt-link as viewer: status = %d, want %d (§13.3: viewers are read-only and cannot link, §29.9)", method, status, http.StatusForbidden)
		}
	}
}

// TestChatGPTLink_StartAndPollHappyPath overrides the rig's own
// chatGPTDeviceFlow (via newTestRig's mutate) with a real fake auth.
// openai.com granting immediately, then drives POST -> GET(linked) ->
// DELETE -> GET(unlinked) through the REAL HTTP routes end to end.
func TestChatGPTLink_StartAndPollHappyPath(t *testing.T) {
	fakeAuthSrv := newFakeChatGPTAuthServer(t, http.StatusOK)

	ctx := context.Background()
	rig := newTestRig(t, func(r *testRig) {
		r.chatGPTDeviceFlow = chatgptoauth.New(http.DefaultClient, fakeAuthSrv, 5*time.Second)
	})
	_, memberToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMember)

	var started chatGPTLinkStatusForTest
	if status := rig.doJSON(t, http.MethodPost, "/api/me/chatgpt-link", nil, &started, memberToken); status != http.StatusOK {
		t.Fatalf("POST /api/me/chatgpt-link status = %d, want %d", status, http.StatusOK)
	}
	if started.Status != "pending" {
		t.Fatalf("POST /api/me/chatgpt-link status field = %q, want %q", started.Status, "pending")
	}
	if started.UserCode == nil || *started.UserCode == "" {
		t.Error("POST /api/me/chatgpt-link userCode is absent/empty, want a real code")
	}

	var polled chatGPTLinkStatusForTest
	if status := rig.doJSON(t, http.MethodGet, "/api/me/chatgpt-link", nil, &polled, memberToken); status != http.StatusOK {
		t.Fatalf("GET /api/me/chatgpt-link status = %d, want %d", status, http.StatusOK)
	}
	if polled.Status != "linked" {
		t.Fatalf("GET /api/me/chatgpt-link (after grant) status field = %q, want %q", polled.Status, "linked")
	}

	if status := rig.doJSON(t, http.MethodDelete, "/api/me/chatgpt-link", nil, nil, memberToken); status != http.StatusNoContent {
		t.Fatalf("DELETE /api/me/chatgpt-link status = %d, want %d", status, http.StatusNoContent)
	}

	var afterUnlink chatGPTLinkStatusForTest
	if status := rig.doJSON(t, http.MethodGet, "/api/me/chatgpt-link", nil, &afterUnlink, memberToken); status != http.StatusOK {
		t.Fatalf("GET /api/me/chatgpt-link (after unlink) status = %d, want %d", status, http.StatusOK)
	}
	if afterUnlink.Status != "unlinked" {
		t.Errorf("GET /api/me/chatgpt-link (after unlink) status field = %q, want %q", afterUnlink.Status, "unlinked")
	}
}

// newFakeChatGPTAuthServer starts a minimal fake auth.openai.com granting
// on the FIRST token-poll (tokenPollStatus == http.StatusOK) -- mirrors
// internal/app/chatgptlink's own identically-shaped fakeAuthServer
// (service_integration_test.go), duplicated here rather than shared
// across package boundaries (an internal test type, not exported).
func newFakeChatGPTAuthServer(t *testing.T, tokenPollStatus int) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/accounts/deviceauth/usercode", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_auth_id": "dev-123", "user_code": "WDJB-MJHT", "interval": "0",
			"expires_at": "2099-01-01T00:00:00Z",
		})
	})
	mux.HandleFunc("/api/accounts/deviceauth/token", func(w http.ResponseWriter, _ *http.Request) {
		if tokenPollStatus != http.StatusOK {
			w.WriteHeader(tokenPollStatus)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authorization_code": "auth-code-xyz", "code_verifier": "verifier-abc",
		})
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, _ *http.Request) {
		header := "eyJhbGciOiJub25lIn0"                                 // {"alg":"none"}, base64url, pre-encoded
		payload := "eyJjaGF0Z3B0X2FjY291bnRfaWQiOiJhY2N0LXh5ei03ODkifQ" // {"chatgpt_account_id":"acct-xyz-789"}, base64url
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access-1", "refresh_token": "refresh-1", "expires_in": 864000,
			"id_token": header + "." + payload + ".sig",
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}
