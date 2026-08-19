package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
	"github.com/khazaddev/narvi/internal/sandboxagent/boot"
	"github.com/khazaddev/narvi/internal/sandboxagent/credentials"
)

// TestApplyOpenCodeConfig_WritesBothDocuments proves the real mechanism:
// the global document lands at <homeDir>/.config/opencode/opencode.json,
// the environment document lands at the given environmentConfigPath, and
// OPENCODE_CONFIG is set to that exact path. Not t.Parallel() -- mutates
// the real OPENCODE_CONFIG process env var. Uses a t.TempDir()-based
// environmentConfigPath rather than the real openCodeEnvironmentConfigPath
// constant (production writes to "/narvi", root-owned/read-only in a
// local dev/test environment) -- applyOpenCodeConfig's own
// environmentConfigPath parameter exists specifically so this is
// possible without touching real filesystem roots.
func TestApplyOpenCodeConfig_WritesBothDocuments(t *testing.T) {
	t.Setenv(openCodeConfigEnvVar, "")
	homeDir := t.TempDir()
	envConfigPath := filepath.Join(t.TempDir(), "opencode-environment-config.json")

	delivery := credentials.OpenCodeConfigDelivery{
		Global:      []byte(`{"autoupdate":true}`),
		Environment: []byte(`{"model":"anthropic/claude"}`),
	}

	applyOpenCodeConfig(delivery, homeDir, envConfigPath)

	globalPath := filepath.Join(homeDir, openCodeGlobalConfigRelPath)
	globalBytes, err := os.ReadFile(globalPath)
	if err != nil {
		t.Fatalf("read global config at %s: %v", globalPath, err)
	}
	if string(globalBytes) != `{"autoupdate":true}` {
		t.Errorf("global config content = %s, want %s", globalBytes, `{"autoupdate":true}`)
	}

	envBytes, err := os.ReadFile(envConfigPath)
	if err != nil {
		t.Fatalf("read environment config at %s: %v", envConfigPath, err)
	}
	if string(envBytes) != `{"model":"anthropic/claude"}` {
		t.Errorf("environment config content = %s, want %s", envBytes, `{"model":"anthropic/claude"}`)
	}
	if got := os.Getenv(openCodeConfigEnvVar); got != envConfigPath {
		t.Errorf("os.Getenv(%s) = %q, want %q", openCodeConfigEnvVar, got, envConfigPath)
	}
}

// TestApplyOpenCodeConfig_GlobalAbsent_OnlyWritesEnvironment proves the
// two writes are independent: an absent global document writes nothing
// to the global slot, but the environment document still lands.
func TestApplyOpenCodeConfig_GlobalAbsent_OnlyWritesEnvironment(t *testing.T) {
	t.Setenv(openCodeConfigEnvVar, "")
	homeDir := t.TempDir()
	envConfigPath := filepath.Join(t.TempDir(), "opencode-environment-config.json")

	delivery := credentials.OpenCodeConfigDelivery{
		Environment: []byte(`{"model":"anthropic/claude"}`),
	}
	applyOpenCodeConfig(delivery, homeDir, envConfigPath)

	globalPath := filepath.Join(homeDir, openCodeGlobalConfigRelPath)
	if _, err := os.Stat(globalPath); err == nil {
		t.Errorf("global config file exists at %s, want absent (delivery.Global was empty)", globalPath)
	}
	if got := os.Getenv(openCodeConfigEnvVar); got != envConfigPath {
		t.Errorf("os.Getenv(%s) = %q, want %q", openCodeConfigEnvVar, got, envConfigPath)
	}
}

// TestApplyOpenCodeConfig_BothAbsent_TouchesNothing proves the
// overwhelming common case (nothing configured at either scope) leaves
// OPENCODE_CONFIG unset and writes no files at all.
func TestApplyOpenCodeConfig_BothAbsent_TouchesNothing(t *testing.T) {
	t.Setenv(openCodeConfigEnvVar, "")
	if err := os.Unsetenv(openCodeConfigEnvVar); err != nil {
		t.Fatalf("Unsetenv(%s): %v", openCodeConfigEnvVar, err)
	}
	homeDir := t.TempDir()
	envConfigPath := filepath.Join(t.TempDir(), "opencode-environment-config.json")

	applyOpenCodeConfig(credentials.OpenCodeConfigDelivery{}, homeDir, envConfigPath)

	globalPath := filepath.Join(homeDir, openCodeGlobalConfigRelPath)
	if _, err := os.Stat(globalPath); err == nil {
		t.Errorf("global config file exists at %s, want absent", globalPath)
	}
	if _, err := os.Stat(envConfigPath); err == nil {
		t.Errorf("environment config file exists at %s, want absent", envConfigPath)
	}
	if got := os.Getenv(openCodeConfigEnvVar); got != "" {
		t.Errorf("os.Getenv(%s) = %q, want empty (unset)", openCodeConfigEnvVar, got)
	}
}

// TestFetchOpenCodeConfig_RealRoundTrip mirrors
// TestFetchSandboxSecrets_RealRoundTrip -- this package's own thin
// wrapper (client construction, logging, degrade-on-failure) over
// CPClient.FetchOpenCodeConfig, whose own wire-shape correctness is
// covered exhaustively in internal/sandboxagent/credentials' own test
// suite.
func TestFetchOpenCodeConfig_RealRoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"global":{"autoupdate":true}}`))
	}))
	defer server.Close()

	cfg := boot.Config{
		SessionConfig: &sessionconfig.SessionConfig{
			ControlPlaneWsUrl: wsEquivalentForTest(server.URL),
			SessionId:         "sess-1",
			SandboxToken:      "tok",
			Gen:               1,
		},
	}

	got := fetchOpenCodeConfig(context.Background(), cfg, testSandboxSecretsFetchTimeout)
	if string(got.Global) != `{"autoupdate":true}` {
		t.Errorf("fetchOpenCodeConfig().Global = %s, want %s", got.Global, `{"autoupdate":true}`)
	}
}

// TestFetchOpenCodeConfig_DegradesToZeroValueOnFailure proves the "warn
// and continue" posture directly.
func TestFetchOpenCodeConfig_DegradesToZeroValueOnFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := boot.Config{
		SessionConfig: &sessionconfig.SessionConfig{
			ControlPlaneWsUrl: wsEquivalentForTest(server.URL),
			SessionId:         "sess-1",
			SandboxToken:      "tok",
			Gen:               1,
		},
	}

	got := fetchOpenCodeConfig(context.Background(), cfg, testSandboxSecretsFetchTimeout)
	if len(got.Global) != 0 || len(got.Environment) != 0 {
		t.Errorf("fetchOpenCodeConfig() = %+v, want zero value on a 500 response", got)
	}
}

// TestFetchOpenCodeConfig_NilSessionConfigPanics mirrors
// TestFetchSandboxSecrets_NilSessionConfigPanics exactly -- see that
// test's own doc comment for the full "never reaches BuildImage"
// reasoning this canary defends.
func TestFetchOpenCodeConfig_NilSessionConfigPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("fetchOpenCodeConfig(cfg with nil SessionConfig) did not panic, want a nil-pointer panic (see this test's own doc comment)")
		}
	}()
	fetchOpenCodeConfig(context.Background(), boot.Config{}, testSandboxSecretsFetchTimeout)
}
