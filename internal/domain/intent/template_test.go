package intent

import (
	"errors"
	"testing"
)

func TestValidateTemplate(t *testing.T) {
	tests := []struct {
		name        string
		tmpl        string
		allowedVars []string
		wantErr     bool
		wantNames   []string
	}{
		{
			name:        "no placeholders",
			tmpl:        "You are a helpful classifier.",
			allowedVars: nil,
			wantErr:     false,
		},
		{
			name:        "single known placeholder",
			tmpl:        "Surface: {{surface}}",
			allowedVars: []string{"surface"},
			wantErr:     false,
		},
		{
			name:        "multiple known placeholders",
			tmpl:        "Surface: {{surface}}. Input: {{input_text}}",
			allowedVars: []string{"surface", "input_text"},
			wantErr:     false,
		},
		{
			name:        "unknown placeholder rejected",
			tmpl:        "Surface: {{surfac}}",
			allowedVars: []string{"surface"},
			wantErr:     true,
			wantNames:   []string{"surfac"},
		},
		{
			name:        "mix of known and unknown",
			tmpl:        "{{surface}} {{typo_var}} {{input_text}}",
			allowedVars: []string{"surface", "input_text"},
			wantErr:     true,
			wantNames:   []string{"typo_var"},
		},
		{
			name:        "same unknown placeholder repeated is reported once",
			tmpl:        "{{bad}} and {{bad}} again",
			allowedVars: nil,
			wantErr:     true,
			wantNames:   []string{"bad"},
		},
		{
			name:        "whitespace inside braces still matches",
			tmpl:        "{{ surface }}",
			allowedVars: []string{"surface"},
			wantErr:     false,
		},
		{
			name:        "allowed var never referenced is fine",
			tmpl:        "no placeholders here",
			allowedVars: []string{"surface", "input_text"},
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTemplate(tt.tmpl, tt.allowedVars)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				var upe *UnknownPlaceholderError
				if !errors.As(err, &upe) {
					t.Fatalf("error is not *UnknownPlaceholderError: %v (%T)", err, err)
				}
				if !stringSlicesEqual(upe.Names, tt.wantNames) {
					t.Errorf("Names = %v, want %v", upe.Names, tt.wantNames)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestAssembleTemplate(t *testing.T) {
	tests := []struct {
		name      string
		tmpl      string
		vars      map[string]string
		want      string
		wantErr   bool
		wantNames []string
	}{
		{
			name: "no placeholders",
			tmpl: "You are a classifier.",
			vars: nil,
			want: "You are a classifier.",
		},
		{
			name: "single substitution",
			tmpl: "Surface: {{surface}}",
			vars: map[string]string{"surface": "github"},
			want: "Surface: github",
		},
		{
			name: "multiple substitutions",
			tmpl: "Surface: {{surface}}. Input: {{input_text}}",
			vars: map[string]string{"surface": "slack", "input_text": "please review this"},
			want: "Surface: slack. Input: please review this",
		},
		{
			name: "same placeholder repeated substitutes every occurrence",
			tmpl: "{{x}}-{{x}}",
			vars: map[string]string{"x": "a"},
			want: "a-a",
		},
		{
			name:    "missing variable fails, does not partially substitute",
			tmpl:    "Surface: {{surface}}. Input: {{input_text}}",
			vars:    map[string]string{"surface": "web"},
			wantErr: true,
		},
		{
			name:      "same missing placeholder repeated is reported once",
			tmpl:      "{{missing}} and {{missing}} again",
			vars:      nil,
			wantErr:   true,
			wantNames: []string{"missing"},
		},
		{
			name:    "extra vars not referenced by the template are simply unused",
			tmpl:    "Surface: {{surface}}",
			vars:    map[string]string{"surface": "linear", "unused": "ignored"},
			want:    "Surface: linear",
			wantErr: false,
		},
		{
			name: "empty-string substitution value is valid",
			tmpl: "[{{x}}]",
			vars: map[string]string{"x": ""},
			want: "[]",
		},
		{
			name: "substituted value can itself contain brace-like text without recursive expansion",
			tmpl: "{{x}}",
			vars: map[string]string{"x": "{{not_a_placeholder}}"},
			want: "{{not_a_placeholder}}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := AssembleTemplate(tt.tmpl, tt.vars)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got result %q", got)
				}
				var upe *UnknownPlaceholderError
				if !errors.As(err, &upe) {
					t.Fatalf("error is not *UnknownPlaceholderError: %v (%T)", err, err)
				}
				if tt.wantNames != nil && !stringSlicesEqual(upe.Names, tt.wantNames) {
					t.Errorf("Names = %v, want %v", upe.Names, tt.wantNames)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
