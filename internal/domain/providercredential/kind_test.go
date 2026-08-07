package providercredential

import "testing"

func TestIsValidKind(t *testing.T) {
	tests := []struct {
		name string
		k    Kind
		want bool
	}{
		{"api_key", KindAPIKey, true},
		{"oauth", KindOAuth, true},
		{"empty", Kind(""), false},
		{"unrecognized", Kind("bearer"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsValidKind(tc.k); got != tc.want {
				t.Errorf("IsValidKind(%q) = %v, want %v", tc.k, got, tc.want)
			}
		})
	}
}

func TestAllKinds_EveryEntryIsValid(t *testing.T) {
	if len(AllKinds) != 2 {
		t.Fatalf("len(AllKinds) = %d, want 2", len(AllKinds))
	}
	for _, k := range AllKinds {
		if !IsValidKind(k) {
			t.Errorf("AllKinds contains %q, which IsValidKind rejects", k)
		}
	}
}
