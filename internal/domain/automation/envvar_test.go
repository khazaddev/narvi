package automation_test

import (
	"errors"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/automation"
)

func TestValidateEnvVars(t *testing.T) {
	maxVars := make([]automation.EnvVar, automation.MaxEnvVars)
	for i := range maxVars {
		maxVars[i] = automation.EnvVar{Name: "VAR" + string(rune('A'+i%26)) + string(rune('0'+i/26)), Value: "v"}
	}
	overMax := append(append([]automation.EnvVar{}, maxVars...), automation.EnvVar{Name: "ONE_MORE", Value: "v"})

	tests := []struct {
		name    string
		vars    []automation.EnvVar
		wantErr error
	}{
		{"nil is valid", nil, nil},
		{"empty is valid", []automation.EnvVar{}, nil},
		{"one valid var", []automation.EnvVar{{Name: "FOO", Value: "bar"}}, nil},
		{"empty value is legitimate", []automation.EnvVar{{Name: "FOO", Value: ""}}, nil},
		{"underscore and digits", []automation.EnvVar{{Name: "_FOO_2", Value: "bar"}}, nil},
		{"empty name", []automation.EnvVar{{Name: "", Value: "bar"}}, automation.ErrEmptyEnvVarName},
		{"name starts with digit", []automation.EnvVar{{Name: "2FOO", Value: "bar"}}, automation.ErrInvalidEnvVarName},
		{"name has dash", []automation.EnvVar{{Name: "FOO-BAR", Value: "bar"}}, automation.ErrInvalidEnvVarName},
		{"name has space", []automation.EnvVar{{Name: "FOO BAR", Value: "bar"}}, automation.ErrInvalidEnvVarName},
		{"duplicate name", []automation.EnvVar{{Name: "FOO", Value: "1"}, {Name: "FOO", Value: "2"}}, automation.ErrDuplicateEnvVarName},
		{"exactly the max", maxVars, nil},
		{"one over the max", overMax, automation.ErrTooManyEnvVars},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := automation.ValidateEnvVars(tt.vars)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("got %v, want %v", err, tt.wantErr)
			}
		})
	}
}
