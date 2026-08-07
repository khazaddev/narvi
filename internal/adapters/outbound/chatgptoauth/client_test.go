package chatgptoauth

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

func TestStartDeviceAuth(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/api/accounts/deviceauth/usercode" {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
			var req usercodeRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if req.ClientID != ClientID {
				t.Errorf("client_id = %q, want %q", req.ClientID, ClientID)
			}
			// interval is a STRING and expires_at is present -- the real,
			// live-verified shape (this package's own usercode canary,
			// usercode_canary_test.go), not §29.2's own original,
			// incomplete field-type assumption.
			_ = json.NewEncoder(w).Encode(usercodeResponse{
				DeviceAuthID: "dev-123", UserCode: "WDJB-MJHT", Interval: "5", ExpiresAt: "2026-08-07T01:48:44.868061+00:00",
			})
		}))
		defer srv.Close()

		c := New(srv.Client(), srv.URL, testTimeout)
		got, err := c.StartDeviceAuth(t.Context())
		if err != nil {
			t.Fatalf("StartDeviceAuth() error = %v, want nil", err)
		}
		wantExpiresAt, err := time.Parse(time.RFC3339Nano, "2026-08-07T01:48:44.868061+00:00")
		if err != nil {
			t.Fatalf("parse want expiresAt: %v", err)
		}
		want := UsercodeResult{DeviceAuthID: "dev-123", UserCode: "WDJB-MJHT", Interval: 5 * time.Second, ExpiresAt: wantExpiresAt}
		if !got.ExpiresAt.Equal(want.ExpiresAt) || got.DeviceAuthID != want.DeviceAuthID || got.UserCode != want.UserCode || got.Interval != want.Interval {
			t.Errorf("StartDeviceAuth() = %+v, want %+v", got, want)
		}
	})

	t.Run("malformed interval is a real error", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(usercodeResponse{DeviceAuthID: "dev-123", UserCode: "WDJB-MJHT", Interval: "not-a-number", ExpiresAt: "2026-08-07T01:48:44.868061+00:00"})
		}))
		defer srv.Close()

		c := New(srv.Client(), srv.URL, testTimeout)
		if _, err := c.StartDeviceAuth(t.Context()); err == nil {
			t.Error("StartDeviceAuth() error = nil, want non-nil for a malformed interval")
		}
	})

	t.Run("malformed expires_at is a real error", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(usercodeResponse{DeviceAuthID: "dev-123", UserCode: "WDJB-MJHT", Interval: "5", ExpiresAt: "not-a-timestamp"})
		}))
		defer srv.Close()

		c := New(srv.Client(), srv.URL, testTimeout)
		if _, err := c.StartDeviceAuth(t.Context()); err == nil {
			t.Error("StartDeviceAuth() error = nil, want non-nil for a malformed expires_at")
		}
	})

	t.Run("server error surfaces as a real error", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		c := New(srv.Client(), srv.URL, testTimeout)
		if _, err := c.StartDeviceAuth(t.Context()); err == nil {
			t.Error("StartDeviceAuth() error = nil, want non-nil for a 500 response")
		}
	})
}

func TestPollDeviceToken(t *testing.T) {
	t.Parallel()

	t.Run("granted", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req deviceTokenRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.DeviceAuthID != "dev-123" || req.UserCode != "WDJB-MJHT" {
				t.Errorf("unexpected request body: %+v", req)
			}
			_ = json.NewEncoder(w).Encode(deviceTokenResponse{AuthorizationCode: "auth-code-xyz", CodeVerifier: "verifier-abc"})
		}))
		defer srv.Close()

		c := New(srv.Client(), srv.URL, testTimeout)
		got, err := c.PollDeviceToken(t.Context(), "dev-123", "WDJB-MJHT")
		if err != nil {
			t.Fatalf("PollDeviceToken() error = %v, want nil", err)
		}
		want := DeviceTokenResult{AuthorizationCode: "auth-code-xyz", CodeVerifier: "verifier-abc"}
		if got != want {
			t.Errorf("PollDeviceToken() = %+v, want %+v", got, want)
		}
	})

	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		t.Run("pending ("+http.StatusText(status)+")", func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			}))
			defer srv.Close()

			c := New(srv.Client(), srv.URL, testTimeout)
			_, err := c.PollDeviceToken(t.Context(), "dev-123", "WDJB-MJHT")
			if !errors.Is(err, ErrDeviceAuthPending) {
				t.Errorf("PollDeviceToken() error = %v, want ErrDeviceAuthPending", err)
			}
		})
	}

	t.Run("real failure is NOT mistaken for pending", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		c := New(srv.Client(), srv.URL, testTimeout)
		_, err := c.PollDeviceToken(t.Context(), "dev-123", "WDJB-MJHT")
		if err == nil {
			t.Fatal("PollDeviceToken() error = nil, want non-nil for a 500 response")
		}
		if errors.Is(err, ErrDeviceAuthPending) {
			t.Error("PollDeviceToken() error wraps ErrDeviceAuthPending for a 500 response, want a real error only")
		}
	})
}

func TestExchangeAuthorizationCode(t *testing.T) {
	t.Parallel()

	idToken := fakeJWT(`{"chatgpt_account_id":"acct-abc-789"}`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Errorf("path = %q, want /oauth/token", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", ct)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		want := url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {"auth-code-xyz"},
			"code_verifier": {"verifier-abc"},
			"redirect_uri":  {deviceAuthCallbackRedirectURI},
			"client_id":     {ClientID},
		}
		for k := range want {
			if r.PostForm.Get(k) != want.Get(k) {
				t.Errorf("form[%q] = %q, want %q", k, r.PostForm.Get(k), want.Get(k))
			}
		}
		_ = json.NewEncoder(w).Encode(tokenResponse{
			AccessToken: "access-1", RefreshToken: "refresh-1", ExpiresIn: 864000, IDToken: idToken,
		})
	}))
	defer srv.Close()

	c := New(srv.Client(), srv.URL, testTimeout)
	got, err := c.ExchangeAuthorizationCode(t.Context(), "auth-code-xyz", "verifier-abc")
	if err != nil {
		t.Fatalf("ExchangeAuthorizationCode() error = %v, want nil", err)
	}
	want := TokenResult{AccessToken: "access-1", RefreshToken: "refresh-1", ExpiresIn: 864000 * time.Second, AccountID: "acct-abc-789"}
	if got != want {
		t.Errorf("ExchangeAuthorizationCode() = %+v, want %+v", got, want)
	}
}

func TestRefreshToken(t *testing.T) {
	t.Parallel()

	// §29.10 risk 7 / client.go's own postToken doc comment: a
	// refresh_token grant's own response is NOT guaranteed to carry a
	// usable id_token the way the initial authorization_code exchange's
	// response does -- this must NOT fail the call; AccountID is simply
	// left "" and internal/app/chatgptrefresh (the only real caller of
	// RefreshToken) must never read it, always preserving the accountId
	// it already had stored instead.
	t.Run("no id_token at all still succeeds, with an empty AccountID", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(tokenResponse{
				AccessToken: "new-access", RefreshToken: "new-refresh", ExpiresIn: 864000,
				// IDToken deliberately omitted.
			})
		}))
		defer srv.Close()

		c := New(srv.Client(), srv.URL, testTimeout)
		got, err := c.RefreshToken(t.Context(), "old-refresh")
		if err != nil {
			t.Fatalf("RefreshToken() error = %v, want nil", err)
		}
		if got.AccessToken != "new-access" || got.RefreshToken != "new-refresh" {
			t.Errorf("RefreshToken() = %+v, want the rotated access/refresh tokens regardless of AccountID", got)
		}
		if got.AccountID != "" {
			t.Errorf("RefreshToken().AccountID = %q, want empty (no id_token in this response)", got.AccountID)
		}
	})

	t.Run("success rotates both tokens", func(t *testing.T) {
		t.Parallel()

		idToken := fakeJWT(`{"chatgpt_account_id":"acct-abc-789"}`)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			if r.PostForm.Get("grant_type") != "refresh_token" {
				t.Errorf("grant_type = %q, want refresh_token", r.PostForm.Get("grant_type"))
			}
			if r.PostForm.Get("refresh_token") != "old-refresh" {
				t.Errorf("refresh_token = %q, want old-refresh", r.PostForm.Get("refresh_token"))
			}
			_ = json.NewEncoder(w).Encode(tokenResponse{
				AccessToken: "new-access", RefreshToken: "new-refresh", ExpiresIn: 864000, IDToken: idToken,
			})
		}))
		defer srv.Close()

		c := New(srv.Client(), srv.URL, testTimeout)
		got, err := c.RefreshToken(t.Context(), "old-refresh")
		if err != nil {
			t.Fatalf("RefreshToken() error = %v, want nil", err)
		}
		if got.AccessToken != "new-access" || got.RefreshToken != "new-refresh" {
			t.Errorf("RefreshToken() = %+v, want new rotated access/refresh tokens", got)
		}
	})

	t.Run("reuse detection surfaces as a terminal TokenError", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(tokenErrorResponse{Error: "refresh_token_reused", ErrorDescription: "token already used"})
		}))
		defer srv.Close()

		c := New(srv.Client(), srv.URL, testTimeout)
		_, err := c.RefreshToken(t.Context(), "already-consumed")
		var tokenErr *TokenError
		if !errors.As(err, &tokenErr) {
			t.Fatalf("RefreshToken() error = %v, want a *TokenError", err)
		}
		if !tokenErr.IsTerminal() {
			t.Errorf("TokenError{Code: %q}.IsTerminal() = false, want true", tokenErr.Code)
		}
	})

	t.Run("transient failure is NOT terminal", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()

		c := New(srv.Client(), srv.URL, testTimeout)
		_, err := c.RefreshToken(t.Context(), "some-refresh-token")
		var tokenErr *TokenError
		if !errors.As(err, &tokenErr) {
			t.Fatalf("RefreshToken() error = %v, want a *TokenError", err)
		}
		if tokenErr.IsTerminal() {
			t.Error("TokenError.IsTerminal() = true for a 503 with no OAuth error body, want false")
		}
	})
}

// TestPostToken_RefusesToFollowRedirects is R1's own regression test
// (adversarial review): POST /oauth/token's own request body carries a
// live authorization code or refresh token (postToken is the shared body
// of both ExchangeAuthorizationCode and RefreshToken). http.Client's own
// DEFAULT redirect policy automatically follows a 307/308 and RE-POSTS
// the identical method+body to wherever it points -- Go strips only
// sensitive HEADERS crossing hosts on a redirect, never the body -- so a
// malicious or compromised token endpoint could exfiltrate a live
// credential via nothing more than a same-request redirect. Proves the
// dedicated tokenEndpointClient (client.go's own New) never follows one:
// the "attacker" server the primary server's own 307 points to must
// receive NO request at all.
func TestPostToken_RefusesToFollowRedirects(t *testing.T) {
	t.Parallel()

	var attackerHit bool
	attacker := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		attackerHit = true
		body, _ := io.ReadAll(r.Body)
		t.Errorf("attacker server received a request it should never have seen (the redirect was followed): body=%s", body)
	}))
	defer attacker.Close()

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL+"/oauth/token", http.StatusTemporaryRedirect)
	}))
	defer primary.Close()

	c := New(primary.Client(), primary.URL, testTimeout)
	_, err := c.RefreshToken(t.Context(), "some-live-refresh-token")
	if err == nil {
		t.Error("RefreshToken() error = nil, want a non-nil error (a 307 is not a valid token response)")
	}
	if attackerHit {
		t.Error("the attacker server was hit -- the redirect was followed, leaking the request body carrying the refresh token")
	}
}
