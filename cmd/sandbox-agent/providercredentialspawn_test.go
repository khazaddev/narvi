// This file closes R3 (adversarial review, cheap hardening): the security-
// critical split main.go's own providerCredentialSpawnEnv/
// providerCredentialOAuthSets encode -- an oauth-kind resolved credential
// must NEVER reach OPENAI_API_KEY (or any other provider's own env var),
// and an api-kind credential must never be delivered via the oauth PUT
// /auth/{providerID} path instead -- had ZERO tests of its own. The code
// (the `if value.Type != "api" || value.Key == nil { continue }` filter in
// providerCredentialSpawnEnv, and its mirror-image `if value.Type ==
// "oauth"` in providerCredentialOAuthSets) is correct and defended in
// depth (§29.6's own "an oauth credential is delivered via PUT
// /auth/{providerID}, never an env var" split), but a regression here --
// e.g. an OAuth access token silently ending up in OPENAI_API_KEY, sent to
// every tool/process in the sandbox rather than scoped to OpenCode's own
// auth store -- would be exactly the kind of credential-handling bug this
// package's own "never logs any resolved credential VALUE" discipline
// exists to avoid, so it deserves a real regression test, not just review
// scrutiny.
package main

import (
	"testing"

	"github.com/khazaddev/narvi/internal/sandboxagent/credentials"
)

func strPtr(s string) *string { return &s }

// TestProviderCredentialSpawnEnv_OAuthEntryContributesNothing proves an
// oauth-kind entry in resolved never reaches providerCredentialSpawnEnv's
// own returned "NAME=VALUE" env slice -- the exact guard
// (opencodeproc.Spawn's own providerCredentialEnv parameter) that keeps an
// OAuth access token out of OPENAI_API_KEY/ANTHROPIC_API_KEY/etc.
func TestProviderCredentialSpawnEnv_OAuthEntryContributesNothing(t *testing.T) {
	tests := []struct {
		name     string
		resolved map[string]credentials.AuthValue
		want     []string
	}{
		{
			name: "an oauth-kind entry alone contributes nothing",
			resolved: map[string]credentials.AuthValue{
				"openai": {Type: "oauth", Access: strPtr("live-access-token"), Expires: int64Ptr(1234567890), AccountID: strPtr("acct-1")},
			},
			want: nil,
		},
		{
			name: "an api-kind entry alone contributes its own env var(s)",
			resolved: map[string]credentials.AuthValue{
				"openai": {Type: "api", Key: strPtr("sk-live-key")},
			},
			want: []string{"OPENAI_API_KEY=sk-live-key"},
		},
		{
			name: "a mixed resolved map: the oauth entry contributes nothing, the api entry still does",
			resolved: map[string]credentials.AuthValue{
				"openai":    {Type: "oauth", Access: strPtr("live-access-token"), Expires: int64Ptr(1234567890), AccountID: strPtr("acct-1")},
				"anthropic": {Type: "api", Key: strPtr("sk-ant-live-key")},
			},
			want: []string{"ANTHROPIC_API_KEY=sk-ant-live-key"},
		},
		{
			name: "an api-kind entry with a nil Key contributes nothing (defensive: never emit \"NAME=\" for a missing value)",
			resolved: map[string]credentials.AuthValue{
				"openai": {Type: "api", Key: nil},
			},
			want: nil,
		},
		{
			name:     "empty resolved map contributes nothing",
			resolved: map[string]credentials.AuthValue{},
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := providerCredentialSpawnEnv(tt.resolved)
			assertStringSlicesEqualUnordered(t, got, tt.want)
		})
	}
}

// TestProviderCredentialOAuthSets_ApiEntryContributesNothing is this
// test's own mirror-image guard: an api-kind entry must never end up in
// providerCredentialOAuthSets' own returned map, which run() PUTs
// verbatim to OpenCode's own auth store (agentRuntime.SetOAuthAuth) --
// an api-kind KEY value landing there would be a credential-type
// confusion in the other direction (a static key handled as if it were a
// live OAuth access token).
func TestProviderCredentialOAuthSets_ApiEntryContributesNothing(t *testing.T) {
	tests := []struct {
		name     string
		resolved map[string]credentials.AuthValue
		want     map[string]credentials.AuthValue
	}{
		{
			name: "an api-kind entry alone contributes nothing",
			resolved: map[string]credentials.AuthValue{
				"openai": {Type: "api", Key: strPtr("sk-live-key")},
			},
			want: map[string]credentials.AuthValue{},
		},
		{
			name: "an oauth-kind entry alone is returned unchanged",
			resolved: map[string]credentials.AuthValue{
				"openai": {Type: "oauth", Access: strPtr("live-access-token"), Expires: int64Ptr(1234567890), AccountID: strPtr("acct-1")},
			},
			want: map[string]credentials.AuthValue{
				"openai": {Type: "oauth", Access: strPtr("live-access-token"), Expires: int64Ptr(1234567890), AccountID: strPtr("acct-1")},
			},
		},
		{
			name: "a mixed resolved map: only the oauth entry survives",
			resolved: map[string]credentials.AuthValue{
				"openai":    {Type: "oauth", Access: strPtr("live-access-token"), Expires: int64Ptr(1234567890), AccountID: strPtr("acct-1")},
				"anthropic": {Type: "api", Key: strPtr("sk-ant-live-key")},
			},
			want: map[string]credentials.AuthValue{
				"openai": {Type: "oauth", Access: strPtr("live-access-token"), Expires: int64Ptr(1234567890), AccountID: strPtr("acct-1")},
			},
		},
		{
			name:     "empty resolved map contributes nothing",
			resolved: map[string]credentials.AuthValue{},
			want:     map[string]credentials.AuthValue{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := providerCredentialOAuthSets(tt.resolved)
			if len(got) != len(tt.want) {
				t.Fatalf("providerCredentialOAuthSets() = %+v (len %d), want %+v (len %d)", got, len(got), tt.want, len(tt.want))
			}
			for provider, wantValue := range tt.want {
				gotValue, ok := got[provider]
				if !ok {
					t.Errorf("providerCredentialOAuthSets() missing provider %q, want %+v", provider, wantValue)
					continue
				}
				if gotValue.Type != wantValue.Type ||
					!strPtrEqual(gotValue.Access, wantValue.Access) ||
					!int64PtrEqual(gotValue.Expires, wantValue.Expires) ||
					!strPtrEqual(gotValue.AccountID, wantValue.AccountID) {
					t.Errorf("providerCredentialOAuthSets()[%q] = %+v, want %+v", provider, gotValue, wantValue)
				}
			}
		})
	}
}

func int64Ptr(i int64) *int64 { return &i }

func strPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func int64PtrEqual(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// assertStringSlicesEqualUnordered compares two []string slices as sets --
// providerCredentialSpawnEnv iterates a Go map internally, so its own
// output order across multiple entries is not guaranteed.
func assertStringSlicesEqualUnordered(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("providerCredentialSpawnEnv() = %v, want %v", got, want)
	}
	remaining := make([]string, len(want))
	copy(remaining, want)
	for _, g := range got {
		found := false
		for i, w := range remaining {
			if g == w {
				remaining = append(remaining[:i], remaining[i+1:]...)
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("providerCredentialSpawnEnv() = %v, want %v (unexpected entry %q)", got, want, g)
		}
	}
}
