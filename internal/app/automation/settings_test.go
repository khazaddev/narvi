package automation

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// discardLogger is a *slog.Logger that writes nowhere -- every test in
// this file needs one (applySandboxSettings/buildRunPrompt both log a
// decode failure rather than erroring), mirroring this codebase's own
// established "a real, harmless logger, not a nil one" convention for
// unit-testing a function that takes a *slog.Logger.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestUnmarshalPathScope(t *testing.T) {
	tests := []struct {
		name    string
		raw     []byte
		want    []string
		wantErr bool
	}{
		{"nil", nil, nil, false},
		{"empty bytes", []byte{}, nil, false},
		{"single pattern", []byte(`["apps/web/**"]`), []string{"apps/web/**"}, false},
		{"multiple patterns", []byte(`["a","b"]`), []string{"a", "b"}, false},
		{"malformed json", []byte(`not json`), nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := unmarshalPathScope(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestUnmarshalEnvVars(t *testing.T) {
	got, err := unmarshalEnvVars([]byte(`[{"name":"FOO","value":"bar"}]`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "FOO" || got[0].Value != "bar" {
		t.Fatalf("got %+v, want [{FOO bar}]", got)
	}

	if got, err := unmarshalEnvVars(nil); err != nil || got != nil {
		t.Fatalf("unmarshalEnvVars(nil) = (%v, %v), want (nil, nil)", got, err)
	}

	if _, err := unmarshalEnvVars([]byte(`not json`)); err == nil {
		t.Fatal("expected an error for malformed JSON, got nil")
	}
}

func TestApplySandboxSettings_UnscopedLeavesRequestUnchanged(t *testing.T) {
	req := restdtos.CreateSessionRequest{}
	row := sqlcgen.Automation{}

	applySandboxSettings(discardLogger(), &req, row)

	if req.PathScope != nil {
		t.Errorf("PathScope = %v, want nil for an unscoped automation", req.PathScope)
	}
	if req.MockConfig != nil {
		t.Errorf("MockConfig = %v, want nil for an unscoped automation", req.MockConfig)
	}
}

func TestApplySandboxSettings_PathScopeAppliedToRequest(t *testing.T) {
	req := restdtos.CreateSessionRequest{}
	row := sqlcgen.Automation{SandboxPathScope: []byte(`["apps/web/**"]`)}

	applySandboxSettings(discardLogger(), &req, row)

	if req.PathScope == nil || len(*req.PathScope) != 1 || (*req.PathScope)[0] != "apps/web/**" {
		t.Fatalf("PathScope = %v, want [apps/web/**]", req.PathScope)
	}
	if req.MockConfig != nil {
		t.Errorf("MockConfig = %v, want nil (mock not configured)", req.MockConfig)
	}
}

func TestApplySandboxSettings_MockConfiguredWithContractsPath(t *testing.T) {
	req := restdtos.CreateSessionRequest{}
	contractsPath := "contracts/custom"
	row := sqlcgen.Automation{SandboxMockConfigured: true, SandboxContractsPath: &contractsPath}

	applySandboxSettings(discardLogger(), &req, row)

	if req.MockConfig == nil {
		t.Fatal("MockConfig = nil, want non-nil")
	}
	if req.MockConfig.ContractsPath == nil || *req.MockConfig.ContractsPath != "contracts/custom" {
		t.Fatalf("MockConfig.ContractsPath = %v, want contracts/custom", req.MockConfig.ContractsPath)
	}
}

func TestApplySandboxSettings_MockConfiguredWithoutContractsPathUsesDefault(t *testing.T) {
	req := restdtos.CreateSessionRequest{}
	row := sqlcgen.Automation{SandboxMockConfigured: true}

	applySandboxSettings(discardLogger(), &req, row)

	if req.MockConfig == nil {
		t.Fatal("MockConfig = nil, want non-nil")
	}
	if req.MockConfig.ContractsPath != nil {
		t.Errorf("MockConfig.ContractsPath = %v, want nil (absent means the default)", req.MockConfig.ContractsPath)
	}
}

func TestApplySandboxSettings_MalformedPathScopeDegradesToUnscoped(t *testing.T) {
	req := restdtos.CreateSessionRequest{}
	row := sqlcgen.Automation{SandboxPathScope: []byte(`not json`)}

	// Must not panic, and must leave the request unscoped rather than
	// blocking this run's own session creation over a malformed
	// AUTOMATION-row column.
	applySandboxSettings(discardLogger(), &req, row)

	if req.PathScope != nil {
		t.Errorf("PathScope = %v, want nil after a decode failure", req.PathScope)
	}
}

func TestBuildRunPrompt_NoEnvVarsReturnsPromptUnchanged(t *testing.T) {
	prompt := "do the thing"
	row := sqlcgen.Automation{Prompt: &prompt, EnvVars: []byte(`[]`)}

	got := buildRunPrompt(discardLogger(), row)
	if got == nil || *got != prompt {
		t.Fatalf("buildRunPrompt() = %v, want %q unchanged", got, prompt)
	}
}

func TestBuildRunPrompt_EnvVarsPrependedAsPreamble(t *testing.T) {
	prompt := "run the audit"
	row := sqlcgen.Automation{Prompt: &prompt, EnvVars: []byte(`[{"name":"TARGET_ENV","value":"staging"}]`)}

	got := buildRunPrompt(discardLogger(), row)
	if got == nil {
		t.Fatal("buildRunPrompt() = nil")
	}
	if !strings.HasPrefix(*got, envVarPreamblePrefix) {
		t.Errorf("buildRunPrompt() = %q, want it to start with %q", *got, envVarPreamblePrefix)
	}
	if !strings.Contains(*got, "TARGET_ENV=staging") {
		t.Errorf("buildRunPrompt() = %q, want it to contain TARGET_ENV=staging", *got)
	}
	if !strings.Contains(*got, prompt) {
		t.Errorf("buildRunPrompt() = %q, want it to still contain the original prompt %q", *got, prompt)
	}
	// The original prompt must come AFTER the preamble, not before.
	if strings.Index(*got, envVarPreamblePrefix) > strings.Index(*got, prompt) {
		t.Errorf("buildRunPrompt() = %q, want the env var preamble before the original prompt text", *got)
	}
}

func TestBuildRunPrompt_MalformedEnvVarsDegradesToPlainPrompt(t *testing.T) {
	prompt := "do the thing"
	row := sqlcgen.Automation{Prompt: &prompt, EnvVars: []byte(`not json`)}

	got := buildRunPrompt(discardLogger(), row)
	if got == nil || *got != prompt {
		t.Fatalf("buildRunPrompt() = %v, want %q unchanged after a decode failure", got, prompt)
	}
}

func TestBuildRunPrompt_NilPromptWithEnvVars(t *testing.T) {
	// Defensive: buildRunPrompt is only ever called by fanout.go when
	// automationRow.Prompt is non-nil, but it must not panic if invoked
	// with a nil one either.
	row := sqlcgen.Automation{EnvVars: []byte(`[{"name":"FOO","value":"bar"}]`)}

	got := buildRunPrompt(discardLogger(), row)
	if got == nil {
		t.Fatal("buildRunPrompt() = nil")
	}
	if !strings.Contains(*got, "FOO=bar") {
		t.Errorf("buildRunPrompt() = %q, want it to contain FOO=bar", *got)
	}
}
