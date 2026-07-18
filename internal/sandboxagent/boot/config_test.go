package boot_test

import (
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/sandboxboot"
	"github.com/khazaddev/narvi/internal/sandboxagent/boot"
)

// validSessionConfigJSON is a well-formed SESSION_CONFIG document (every
// required field per contracts/session-config/v1/session-config.schema.json
// present) whose bootMode is "fresh" -- used by every NARVI_SESSION_CONFIG
// test below that needs a valid document, mutating just bootMode where a
// mismatch is wanted.
const validSessionConfigJSON = `{
	"bootMode": "fresh",
	"controlPlaneWsUrl": "wss://cp.example.com/sessions/sess-1/ws?type=sandbox",
	"correlationId": null,
	"gen": 1,
	"repos": [{"name": "repo1", "url": "https://example.com/repo1.git", "branch": null}],
	"sandboxToken": "tok-123",
	"sessionId": "sess-1"
}`

// These tests use t.Setenv, which the testing package forbids combining
// with t.Parallel() (env vars are process-global) -- so none of them call
// t.Parallel.

func TestLoad_MissingBootMode(t *testing.T) {
	t.Setenv("NARVI_BOOT_MODE", "")

	_, err := boot.Load()
	if err == nil {
		t.Fatal("Load() error = nil, want an error for missing NARVI_BOOT_MODE")
	}

	var invErr *sandboxboot.InvalidBootModeError
	if !errors.As(err, &invErr) {
		t.Fatalf("Load() error = %v (%T), want it to wrap *sandboxboot.InvalidBootModeError", err, err)
	}
}

func TestLoad_InvalidBootMode(t *testing.T) {
	t.Setenv("NARVI_BOOT_MODE", "garbage")

	_, err := boot.Load()
	if err == nil {
		t.Fatal("Load() error = nil, want an error for invalid NARVI_BOOT_MODE")
	}

	var invErr *sandboxboot.InvalidBootModeError
	if !errors.As(err, &invErr) {
		t.Fatalf("Load() error = %v (%T), want it to wrap *sandboxboot.InvalidBootModeError", err, err)
	}
}

func TestLoad_DefaultsWhenOptionalVarsAbsent(t *testing.T) {
	t.Setenv("NARVI_BOOT_MODE", "fresh")
	t.Setenv("NARVI_AGENT_VERSION", "")
	t.Setenv("NARVI_IMAGE_DIGEST", "")
	t.Setenv("NARVI_WORKSPACE_DIR", "")
	t.Setenv("NARVI_LOG_LEVEL", "")

	cfg, err := boot.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	if cfg.BootMode != sandboxboot.BootModeFresh {
		t.Errorf("BootMode = %q, want %q", cfg.BootMode, sandboxboot.BootModeFresh)
	}
	if cfg.AgentVersion != "dev" {
		t.Errorf("AgentVersion = %q, want %q", cfg.AgentVersion, "dev")
	}
	if cfg.ImageDigest != "unknown" {
		t.Errorf("ImageDigest = %q, want %q", cfg.ImageDigest, "unknown")
	}
	if cfg.WorkspaceDir != "/workspace" {
		t.Errorf("WorkspaceDir = %q, want %q", cfg.WorkspaceDir, "/workspace")
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelInfo)
	}
}

func TestLoad_AllVarsPresent(t *testing.T) {
	t.Setenv("NARVI_BOOT_MODE", "repo_image")
	t.Setenv("NARVI_AGENT_VERSION", "1.2.3")
	t.Setenv("NARVI_IMAGE_DIGEST", "sha256:deadbeef")
	t.Setenv("NARVI_WORKSPACE_DIR", "/custom/workspace")
	t.Setenv("NARVI_LOG_LEVEL", "debug")

	cfg, err := boot.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	if cfg.BootMode != sandboxboot.BootModeRepoImage {
		t.Errorf("BootMode = %q, want %q", cfg.BootMode, sandboxboot.BootModeRepoImage)
	}
	if cfg.AgentVersion != "1.2.3" {
		t.Errorf("AgentVersion = %q, want %q", cfg.AgentVersion, "1.2.3")
	}
	if cfg.ImageDigest != "sha256:deadbeef" {
		t.Errorf("ImageDigest = %q, want %q", cfg.ImageDigest, "sha256:deadbeef")
	}
	if cfg.WorkspaceDir != "/custom/workspace" {
		t.Errorf("WorkspaceDir = %q, want %q", cfg.WorkspaceDir, "/custom/workspace")
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelDebug)
	}
}

func TestLoad_InvalidLogLevel(t *testing.T) {
	t.Setenv("NARVI_BOOT_MODE", "fresh")
	t.Setenv("NARVI_LOG_LEVEL", "trace")

	_, err := boot.Load()
	if err == nil {
		t.Fatal("Load() error = nil, want an error for invalid NARVI_LOG_LEVEL")
	}

	var invErr *boot.InvalidLogLevelError
	if !errors.As(err, &invErr) {
		t.Fatalf("Load() error = %v (%T), want *boot.InvalidLogLevelError", err, err)
	}
	if invErr.Error() == "" {
		t.Errorf("InvalidLogLevelError.Error() = %q, want a non-empty message", invErr.Error())
	}
}

func TestLoad_CredentialCacheDirDefault(t *testing.T) {
	t.Setenv("NARVI_BOOT_MODE", "fresh")
	t.Setenv("NARVI_CREDENTIAL_CACHE_DIR", "")

	cfg, err := boot.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.CredentialCacheDir != "/tmp/narvi-credentials" {
		t.Errorf("CredentialCacheDir = %q, want %q", cfg.CredentialCacheDir, "/tmp/narvi-credentials")
	}
}

func TestLoad_CredentialCacheDirOverride(t *testing.T) {
	t.Setenv("NARVI_BOOT_MODE", "fresh")
	t.Setenv("NARVI_CREDENTIAL_CACHE_DIR", "/custom/creds")

	cfg, err := boot.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.CredentialCacheDir != "/custom/creds" {
		t.Errorf("CredentialCacheDir = %q, want %q", cfg.CredentialCacheDir, "/custom/creds")
	}
}

func TestLoad_SandboxIDDefault(t *testing.T) {
	t.Setenv("NARVI_BOOT_MODE", "fresh")
	t.Setenv("NARVI_SANDBOX_ID", "")

	cfg, err := boot.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.SandboxID != "" {
		t.Errorf("SandboxID = %q, want empty string (HONEST GAP default)", cfg.SandboxID)
	}
}

func TestLoad_SandboxIDOverride(t *testing.T) {
	t.Setenv("NARVI_BOOT_MODE", "fresh")
	t.Setenv("NARVI_SANDBOX_ID", "sbx-abc123")

	cfg, err := boot.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.SandboxID != "sbx-abc123" {
		t.Errorf("SandboxID = %q, want %q", cfg.SandboxID, "sbx-abc123")
	}
}

// TestLoad_SessionConfigAbsent proves NARVI_SESSION_CONFIG's absence
// remains a fully valid, correct state: SessionConfig is nil and every
// other field behaves exactly as it did before this Step (Steps 13/14's
// own tests, unmodified, already cover that).
func TestLoad_SessionConfigAbsent(t *testing.T) {
	t.Setenv("NARVI_BOOT_MODE", "fresh")
	t.Setenv("NARVI_SESSION_CONFIG", "")

	cfg, err := boot.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.SessionConfig != nil {
		t.Errorf("SessionConfig = %+v, want nil", cfg.SessionConfig)
	}
}

// TestLoad_SessionConfigPresentAndValid proves a well-formed
// NARVI_SESSION_CONFIG document whose bootMode agrees with NARVI_BOOT_MODE
// parses into a populated Config.SessionConfig.
func TestLoad_SessionConfigPresentAndValid(t *testing.T) {
	t.Setenv("NARVI_BOOT_MODE", "fresh")
	t.Setenv("NARVI_SESSION_CONFIG", validSessionConfigJSON)

	cfg, err := boot.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.SessionConfig == nil {
		t.Fatal("SessionConfig = nil, want a populated *SessionConfig")
	}
	if cfg.SessionConfig.SessionId != "sess-1" {
		t.Errorf("SessionConfig.SessionId = %q, want %q", cfg.SessionConfig.SessionId, "sess-1")
	}
	if len(cfg.SessionConfig.Repos) != 1 || cfg.SessionConfig.Repos[0].Name != "repo1" {
		t.Errorf("SessionConfig.Repos = %+v, want one repo named %q", cfg.SessionConfig.Repos, "repo1")
	}
}

// TestLoad_SessionConfigMalformedJSON proves a malformed NARVI_SESSION_CONFIG
// document is a real, propagated error (fail-fast, matching every other
// Load() failure mode) -- not silently ignored.
func TestLoad_SessionConfigMalformedJSON(t *testing.T) {
	t.Setenv("NARVI_BOOT_MODE", "fresh")
	t.Setenv("NARVI_SESSION_CONFIG", "{not valid json")

	_, err := boot.Load()
	if err == nil {
		t.Fatal("Load() error = nil, want an error for malformed NARVI_SESSION_CONFIG")
	}
}

// TestLoad_SessionConfigMissingRequiredField proves a SESSION_CONFIG
// document missing a required field surfaces the generated UnmarshalJSON's
// own error, unwrapped and un-rehashed -- Load() must not swallow or
// re-wrap it into something less informative.
func TestLoad_SessionConfigMissingRequiredField(t *testing.T) {
	t.Setenv("NARVI_BOOT_MODE", "fresh")
	// sessionId is omitted entirely.
	t.Setenv("NARVI_SESSION_CONFIG", `{
		"bootMode": "fresh",
		"controlPlaneWsUrl": "wss://cp.example.com/sessions/sess-1/ws?type=sandbox",
		"correlationId": null,
		"gen": 1,
		"repos": [{"name": "repo1", "url": "https://example.com/repo1.git", "branch": null}],
		"sandboxToken": "tok-123"
	}`)

	_, err := boot.Load()
	if err == nil {
		t.Fatal("Load() error = nil, want an error for a SESSION_CONFIG document missing sessionId")
	}
	if !strings.Contains(err.Error(), "sessionId") {
		t.Errorf("Load() error = %v, want it to name the missing field %q", err, "sessionId")
	}
}

// TestLoad_SessionConfigBootModeMismatch proves a valid SESSION_CONFIG
// document whose bootMode disagrees with the separately-read
// NARVI_BOOT_MODE is a fail-fast *ModeMismatchError.
func TestLoad_SessionConfigBootModeMismatch(t *testing.T) {
	t.Setenv("NARVI_BOOT_MODE", "repo_image")
	t.Setenv("NARVI_SESSION_CONFIG", validSessionConfigJSON) // bootMode: "fresh"

	_, err := boot.Load()
	if err == nil {
		t.Fatal("Load() error = nil, want *boot.ModeMismatchError")
	}

	var mismatchErr *boot.ModeMismatchError
	if !errors.As(err, &mismatchErr) {
		t.Fatalf("Load() error = %v (%T), want *boot.ModeMismatchError", err, err)
	}
	if mismatchErr.EnvValue != "repo_image" {
		t.Errorf("EnvValue = %q, want %q", mismatchErr.EnvValue, "repo_image")
	}
	if mismatchErr.SessionConfigValue != "fresh" {
		t.Errorf("SessionConfigValue = %q, want %q", mismatchErr.SessionConfigValue, "fresh")
	}
	if mismatchErr.Error() == "" {
		t.Errorf("ModeMismatchError.Error() = %q, want a non-empty message", mismatchErr.Error())
	}
}
