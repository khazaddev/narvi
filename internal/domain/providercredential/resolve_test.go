package providercredential

import "testing"

func TestResolve(t *testing.T) {
	tests := []struct {
		name       string
		candidates []Candidate[string]
		wantValue  string
		wantOK     bool
	}{
		{
			name:       "no candidates at all",
			candidates: nil,
			wantOK:     false,
		},
		{
			name:       "only global",
			candidates: []Candidate[string]{{Scope: ScopeGlobal, Value: "global-value"}},
			wantValue:  "global-value",
			wantOK:     true,
		},
		{
			name:       "only repo",
			candidates: []Candidate[string]{{Scope: ScopeRepo, Value: "repo-value"}},
			wantValue:  "repo-value",
			wantOK:     true,
		},
		{
			name:       "only environment",
			candidates: []Candidate[string]{{Scope: ScopeEnvironment, Value: "env-value"}},
			wantValue:  "env-value",
			wantOK:     true,
		},
		{
			name: "repo beats global",
			candidates: []Candidate[string]{
				{Scope: ScopeGlobal, Value: "global-value"},
				{Scope: ScopeRepo, Value: "repo-value"},
			},
			wantValue: "repo-value",
			wantOK:    true,
		},
		{
			name: "environment beats repo -- the doubly-confirmed, non-paraphrase order",
			candidates: []Candidate[string]{
				{Scope: ScopeRepo, Value: "repo-value"},
				{Scope: ScopeEnvironment, Value: "env-value"},
			},
			wantValue: "env-value",
			wantOK:    true,
		},
		{
			name: "environment beats global",
			candidates: []Candidate[string]{
				{Scope: ScopeGlobal, Value: "global-value"},
				{Scope: ScopeEnvironment, Value: "env-value"},
			},
			wantValue: "env-value",
			wantOK:    true,
		},
		{
			name: "all 3 present -- environment wins over both",
			candidates: []Candidate[string]{
				{Scope: ScopeGlobal, Value: "global-value"},
				{Scope: ScopeRepo, Value: "repo-value"},
				{Scope: ScopeEnvironment, Value: "env-value"},
			},
			wantValue: "env-value",
			wantOK:    true,
		},
		{
			name: "all 3 present, input order reversed -- same winner regardless of input order",
			candidates: []Candidate[string]{
				{Scope: ScopeEnvironment, Value: "env-value"},
				{Scope: ScopeRepo, Value: "repo-value"},
				{Scope: ScopeGlobal, Value: "global-value"},
			},
			wantValue: "env-value",
			wantOK:    true,
		},
		{
			name: "two repo candidates -- first in input order wins the tie",
			candidates: []Candidate[string]{
				{Scope: ScopeRepo, Value: "primary-repo-value"},
				{Scope: ScopeRepo, Value: "secondary-repo-value"},
			},
			wantValue: "primary-repo-value",
			wantOK:    true,
		},
		{
			name:       "unrecognized scope is ignored, not an error",
			candidates: []Candidate[string]{{Scope: Scope("bogus"), Value: "should-never-win"}},
			wantOK:     false,
		},
		{
			name: "unrecognized scope ignored alongside a real one",
			candidates: []Candidate[string]{
				{Scope: Scope("bogus"), Value: "should-never-win"},
				{Scope: ScopeGlobal, Value: "global-value"},
			},
			wantValue: "global-value",
			wantOK:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotValue, gotOK := Resolve(tc.candidates)
			if gotOK != tc.wantOK {
				t.Fatalf("ok = %v, want %v", gotOK, tc.wantOK)
			}
			if gotOK && gotValue != tc.wantValue {
				t.Errorf("value = %q, want %q", gotValue, tc.wantValue)
			}
		})
	}
}

// TestResolve_GenericOverStructType proves Resolve works over a non-string
// T too -- the delivery endpoint's real use is Candidate[encryptedRow] (a
// struct), not Candidate[string]; this locks in that the generic actually
// works over a struct payload, not just the scalar this file's own table
// above exercises for readability.
func TestResolve_GenericOverStructType(t *testing.T) {
	type row struct {
		ID             string
		ValueEncrypted []byte
	}

	candidates := []Candidate[row]{
		{Scope: ScopeGlobal, Value: row{ID: "global-id", ValueEncrypted: []byte("global-ciphertext")}},
		{Scope: ScopeEnvironment, Value: row{ID: "env-id", ValueEncrypted: []byte("env-ciphertext")}},
	}

	got, ok := Resolve(candidates)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if got.ID != "env-id" {
		t.Errorf("ID = %q, want %q (environment outranks global)", got.ID, "env-id")
	}
}
