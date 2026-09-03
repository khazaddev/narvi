package automation_test

import (
	"errors"
	"testing"

	"github.com/narvidev/narvi/internal/domain/automation"
	"github.com/narvidev/narvi/internal/domain/environment"
)

func TestIsUnscoped(t *testing.T) {
	tests := []struct {
		name string
		s    automation.SandboxSettings
		want bool
	}{
		{"zero value", automation.SandboxSettings{}, true},
		{"path scope set", automation.SandboxSettings{PathScope: []string{"apps/web/**"}}, false},
		{"mock configured", automation.SandboxSettings{MockConfigured: true}, false},
		{"both set", automation.SandboxSettings{PathScope: []string{"apps/web/**"}, MockConfigured: true}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := automation.IsUnscoped(tt.s); got != tt.want {
				t.Fatalf("IsUnscoped() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateSandboxSettings(t *testing.T) {
	tests := []struct {
		name    string
		s       automation.SandboxSettings
		wantErr bool
	}{
		{"zero value valid", automation.SandboxSettings{}, false},
		{"valid path scope", automation.SandboxSettings{PathScope: []string{"apps/web/**"}}, false},
		{"invalid path scope traversal", automation.SandboxSettings{PathScope: []string{"../etc"}}, true},
		{"mock configured, no contracts path (default)", automation.SandboxSettings{MockConfigured: true}, false},
		{"mock configured, valid contracts path", automation.SandboxSettings{MockConfigured: true, ContractsPath: "contracts/api"}, false},
		{"mock configured, invalid contracts path", automation.SandboxSettings{MockConfigured: true, ContractsPath: "../secrets"}, true},
		{"contracts path set but mock NOT configured is never validated", automation.SandboxSettings{ContractsPath: "../secrets"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := automation.ValidateSandboxSettings(tt.s)
			if tt.wantErr && err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestValidateSandboxSettingsReusesEnvironmentPackage confirms
// ValidateSandboxSettings really does delegate to environment.
// ValidatePathScope rather than reimplementing an independent check -- a
// pattern this package's own doc comment claims but a plain black-box test
// of the two error messages/sentinels alone would not otherwise prove.
func TestValidateSandboxSettingsReusesEnvironmentPackage(t *testing.T) {
	direct := environment.ValidatePathScope([]string{"../etc"})
	if direct == nil {
		t.Fatal("expected environment.ValidatePathScope to reject a traversal pattern")
	}

	err := automation.ValidateSandboxSettings(automation.SandboxSettings{PathScope: []string{"../etc"}})
	if err == nil {
		t.Fatal("expected an error")
	}
	var globErr *environment.InvalidGlobError
	if !errors.As(err, &globErr) {
		t.Fatalf("expected error to wrap *environment.InvalidGlobError, got %v", err)
	}
}
