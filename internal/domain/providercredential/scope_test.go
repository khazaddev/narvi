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
	if len(AllScopes) != 3 {
		t.Fatalf("len(AllScopes) = %d, want 3", len(AllScopes))
	}
	for _, s := range AllScopes {
		if !IsValidScope(s) {
			t.Errorf("AllScopes contains %q, which IsValidScope rejects", s)
		}
	}
}
