package providercredential

import "testing"

func TestIsValidScope(t *testing.T) {
	tests := []struct {
		name string
		s    Scope
		want bool
	}{
		{"repo", ScopeRepo, true},
		{"environment", ScopeEnvironment, true},
		{"global", ScopeGlobal, true},
		{"user", ScopeUser, true},
		{"automation", ScopeAutomation, true},
		{"empty", Scope(""), false},
		{"unrecognized", Scope("bogus"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsValidScope(tc.s); got != tc.want {
				t.Errorf("IsValidScope(%q) = %v, want %v", tc.s, got, tc.want)
			}
		})
	}
}

func TestRequiresScopeTarget(t *testing.T) {
	tests := []struct {
		name string
		s    Scope
		want bool
	}{
		{"repo requires a target", ScopeRepo, true},
		{"environment requires a target", ScopeEnvironment, true},
		{"user requires a target", ScopeUser, true},
		{"automation requires a target", ScopeAutomation, true},
		{"global requires no target", ScopeGlobal, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := RequiresScopeTarget(tc.s); got != tc.want {
				t.Errorf("RequiresScopeTarget(%q) = %v, want %v", tc.s, got, tc.want)
			}
		})
	}
}

func TestAllScopes_EveryEntryIsValid(t *testing.T) {
	if len(AllScopes) != 5 {
		t.Fatalf("len(AllScopes) = %d, want 5", len(AllScopes))
	}
	for _, s := range AllScopes {
		if !IsValidScope(s) {
			t.Errorf("AllScopes contains %q, which IsValidScope rejects", s)
		}
	}
}

// TestScopeUser_IsMostSpecific pins §29.4's own explicit ordering
// requirement ("a personally-linked account is more specific than any
// environment/repo/global org key") -- ScopeUser must outrank every
// provider_credentials-reachable Scope in scopePriority, verified
// indirectly (scopePriority is unexported) via Resolve itself: a
// ScopeUser candidate must win against one candidate of every OTHER
// Scope a provider_credentials row can actually carry, all present at
// once (deliberately excluding ScopeAutomation here -- provider_
// credentials never has an automation-scoped row, so a real candidate
// slice never contains both; see TestScopeAutomation_IsMostSpecific
// below for sandbox_secrets' own analogous, independent claim).
func TestScopeUser_IsMostSpecific(t *testing.T) {
	got, ok := Resolve([]Candidate[string]{
		{Scope: ScopeGlobal, Value: "global"},
		{Scope: ScopeRepo, Value: "repo"},
		{Scope: ScopeEnvironment, Value: "environment"},
		{Scope: ScopeUser, Value: "user"},
	})
	if !ok || got != "user" {
		t.Errorf("Resolve with all 4 provider_credentials scopes present = (%q, %v), want (\"user\", true)", got, ok)
	}
}

// TestScopeAutomation_IsMostSpecific pins §27.1's own explicit ordering
// requirement for sandbox_secrets ("automation -> environment -> repo ->
// global, most specific wins") -- ScopeAutomation must outrank every
// sandbox_secrets-reachable Scope, mirroring TestScopeUser_IsMostSpecific
// immediately above exactly, for the OTHER table's own 4 scopes
// (deliberately excluding ScopeUser -- sandbox_secrets never has a
// user-scoped row).
func TestScopeAutomation_IsMostSpecific(t *testing.T) {
	got, ok := Resolve([]Candidate[string]{
		{Scope: ScopeGlobal, Value: "global"},
		{Scope: ScopeRepo, Value: "repo"},
		{Scope: ScopeEnvironment, Value: "environment"},
		{Scope: ScopeAutomation, Value: "automation"},
	})
	if !ok || got != "automation" {
		t.Errorf("Resolve with all 4 sandbox_secrets scopes present = (%q, %v), want (\"automation\", true)", got, ok)
	}
}
