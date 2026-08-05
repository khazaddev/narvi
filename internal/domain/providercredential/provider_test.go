package providercredential

import (
	"reflect"
	"testing"
)

func TestIsValidProvider(t *testing.T) {
	tests := []struct {
		name string
		p    Provider
		want bool
	}{
		{"google", ProviderGoogle, true},
		{"anthropic", ProviderAnthropic, true},
		{"openai", ProviderOpenAI, true},
		{"empty", Provider(""), false},
		{"unrecognized", Provider("mistral"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsValidProvider(tc.p); got != tc.want {
				t.Errorf("IsValidProvider(%q) = %v, want %v", tc.p, got, tc.want)
			}
		})
	}
}

func TestEnvVarNames(t *testing.T) {
	tests := []struct {
		name string
		p    Provider
		want []string
	}{
		{"google maps to all 3 catalog names", ProviderGoogle, []string{"GOOGLE_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY", "GEMINI_API_KEY"}},
		{"anthropic maps to 1 name", ProviderAnthropic, []string{"ANTHROPIC_API_KEY"}},
		{"openai maps to 1 name", ProviderOpenAI, []string{"OPENAI_API_KEY"}},
		{"unrecognized provider maps to nil", Provider("mistral"), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EnvVarNames(tc.p)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("EnvVarNames(%q) = %v, want %v", tc.p, got, tc.want)
			}
		})
	}
}

// TestEnvVarNames_ReturnsDefensiveCopy proves a caller mutating the
// returned slice can never corrupt what a LATER call returns -- EnvVarNames'
// own doc comment promises a defensive copy, not the shared backing array.
func TestEnvVarNames_ReturnsDefensiveCopy(t *testing.T) {
	got := EnvVarNames(ProviderGoogle)
	got[0] = "MUTATED"

	again := EnvVarNames(ProviderGoogle)
	if again[0] != "GOOGLE_API_KEY" {
		t.Errorf("second call's first element = %q, want %q (mutating a previous return value must not leak through)", again[0], "GOOGLE_API_KEY")
	}
}

func TestAllProviders_EveryEntryIsValid(t *testing.T) {
	if len(AllProviders) != 3 {
		t.Fatalf("len(AllProviders) = %d, want 3", len(AllProviders))
	}
	for _, p := range AllProviders {
		if !IsValidProvider(p) {
			t.Errorf("AllProviders contains %q, which IsValidProvider rejects", p)
		}
	}
}
