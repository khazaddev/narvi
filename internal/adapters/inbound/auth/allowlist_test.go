package auth_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/inbound/auth"
)

// TestAllowlistConfig_EmailAllowed is table-driven over the exact-email and
// email-domain checks (case-insensitively), independent of the
// org-membership check (which needs a live API call and is covered by
// auth_integration_test.go instead).
func TestAllowlistConfig_EmailAllowed(t *testing.T) {
	t.Parallel()

	allowlist := auth.AllowlistConfig{
		EmailDomains: []string{"example.com"},
		Emails:       []string{"vip@other.com"},
	}

	tests := []struct {
		name  string
		email string
		want  bool
	}{
		{name: "exact email match", email: "vip@other.com", want: true},
		{name: "exact email match case-insensitive", email: "VIP@Other.com", want: true},
		{name: "domain match", email: "someone@example.com", want: true},
		{name: "domain match case-insensitive", email: "someone@Example.COM", want: true},
		{name: "neither matches", email: "someone@elsewhere.com", want: false},
		{name: "subdomain does not match a bare domain entry", email: "someone@sub.example.com", want: false},
		{name: "malformed email with no @ never matches", email: "not-an-email", want: false},
		{name: "empty email never matches", email: "", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := allowlist.EmailAllowed(tc.email); got != tc.want {
				t.Errorf("EmailAllowed(%q) = %v, want %v", tc.email, got, tc.want)
			}
		})
	}
}

// TestAllowlistConfig_EmailAllowed_AllEmpty proves an allowlist with all 3
// mechanisms empty allows nothing (the actual "empty allowlist" footgun
// guard lives in platform.Load's own EmptyAllowlistError, not here -- this
// just proves EmailAllowed itself has no permissive fallback).
func TestAllowlistConfig_EmailAllowed_AllEmpty(t *testing.T) {
	t.Parallel()

	var allowlist auth.AllowlistConfig
	if allowlist.EmailAllowed("anyone@anywhere.com") {
		t.Error("EmailAllowed() = true for a zero-value AllowlistConfig, want false")
	}
}
