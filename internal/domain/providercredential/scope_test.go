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
		{"empty", Scope(""), false},
		{"unrecognized", Scope("automation"), false},
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
	if len(AllScopes) != 4 {
		t.Fatalf("len(AllScopes) = %d, want 4", len(AllScopes))
	}
	for _, s := range AllScopes {
		if !IsValidScope(s) {
			t.Errorf("AllScopes contains %q, which IsValidScope rejects", s)
		}
	}
}

// TestScopeUser_IsMostSpecific pins §29.4's own explicit ordering
// requirement ("a personally-linked account is more specific than any
// environment/repo/global org key") -- ScopeUser must outrank every other
// Scope in scopePriority, verified indirectly (scopePriority is
// unexported) via Resolve itself: a ScopeUser candidate must win against
// one candidate of every other Scope, all present at once.
func TestScopeUser_IsMostSpecific(t *testing.T) {
	got, ok := Resolve([]Candidate[string]{
		{Scope: ScopeGlobal, Value: "global"},
		{Scope: ScopeRepo, Value: "repo"},
		{Scope: ScopeEnvironment, Value: "environment"},
		{Scope: ScopeUser, Value: "user"},
	})
	if !ok || got != "user" {
		t.Errorf("Resolve with all 4 scopes present = (%q, %v), want (\"user\", true)", got, ok)
	}
}
