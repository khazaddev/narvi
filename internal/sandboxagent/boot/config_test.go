package boot_test

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/sandboxboot"
	"github.com/khazaddev/narvi/internal/sandboxagent/boot"
)

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
