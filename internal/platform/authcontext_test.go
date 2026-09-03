package platform_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/narvidev/narvi/internal/platform"
)

// TestWithUserContextHelpers covers WithUser/UserFromContext round-trip and
// the "absent" case on a bare context, mirroring
// TestWithCorrelationIDContextHelpers' own precedent (correlation_test.go)
// exactly.
func TestWithUserContextHelpers(t *testing.T) {
	ctx := t.Context()

	if _, ok := platform.UserFromContext(ctx); ok {
		t.Fatal("expected no authenticated user in a bare context")
	}

	want := platform.AuthenticatedUser{ID: "user-123", Role: "member", Email: "dev@example.com"}
	ctx2 := platform.WithUser(ctx, want)

	got, ok := platform.UserFromContext(ctx2)
	if !ok {
		t.Fatal("UserFromContext() ok = false, want true")
	}
	if got != want {
		t.Fatalf("UserFromContext() = %+v, want %+v", got, want)
	}
}

// TestWithAuthSessionCookie covers every security-relevant attribute
// design decision 3 specifies: HttpOnly, SameSite=Lax, Path=/, no Domain
// set, and Secure toggled by the secure parameter (never weakened for
// convenience — the same cookie-issuing code path is exercised whether the
// caller passes true or false).
func TestWithAuthSessionCookie(t *testing.T) {
	t.Parallel()

	expiresAt := time.Now().Add(30 * 24 * time.Hour)

	tests := []struct {
		name   string
		secure bool
	}{
		{name: "secure (production/staging)", secure: true},
		{name: "not secure (development)", secure: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cookie := platform.WithAuthSessionCookie("plaintext-token-value", expiresAt, tc.secure)

			if cookie.Name != platform.AuthSessionCookieName {
				t.Errorf("Name = %q, want %q", cookie.Name, platform.AuthSessionCookieName)
			}
			if cookie.Value != "plaintext-token-value" {
				t.Errorf("Value = %q, want the plaintext token", cookie.Value)
			}
			if !cookie.HttpOnly {
				t.Error("HttpOnly = false, want true")
			}
			if cookie.SameSite != http.SameSiteLaxMode {
				t.Errorf("SameSite = %v, want http.SameSiteLaxMode", cookie.SameSite)
			}
			if cookie.Path != "/" {
				t.Errorf("Path = %q, want \"/\"", cookie.Path)
			}
			if cookie.Domain != "" {
				t.Errorf("Domain = %q, want empty (host-scoped, never set)", cookie.Domain)
			}
			if cookie.Secure != tc.secure {
				t.Errorf("Secure = %v, want %v", cookie.Secure, tc.secure)
			}
			if !cookie.Expires.Equal(expiresAt) {
				t.Errorf("Expires = %v, want %v", cookie.Expires, expiresAt)
			}
		})
	}
}

// TestExpiredAuthSessionCookie covers the logout-clearing cookie: same
// name/attributes as WithAuthSessionCookie (so the browser recognizes it as
// clearing the SAME cookie), empty value, and MaxAge < 0 (the standard
// "delete this cookie now" idiom).
func TestExpiredAuthSessionCookie(t *testing.T) {
	t.Parallel()

	cookie := platform.ExpiredAuthSessionCookie(true)

	if cookie.Name != platform.AuthSessionCookieName {
		t.Errorf("Name = %q, want %q", cookie.Name, platform.AuthSessionCookieName)
	}
	if cookie.Value != "" {
		t.Errorf("Value = %q, want empty", cookie.Value)
	}
	if cookie.MaxAge >= 0 {
		t.Errorf("MaxAge = %d, want < 0 (expire immediately)", cookie.MaxAge)
	}
	if !cookie.HttpOnly {
		t.Error("HttpOnly = false, want true")
	}
	if cookie.Domain != "" {
		t.Errorf("Domain = %q, want empty (host-scoped, never set)", cookie.Domain)
	}
}
