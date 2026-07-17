package platform_test

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/khazaddev/narvi/internal/platform"
)

// TestLoad is table-driven over NARVI_STAGE values: unset, each of the
// three valid stages, and an invalid one. Load must succeed for the first
// four and fail fast with a *platform.InvalidStageError for the last.
func TestLoad(t *testing.T) {
	tests := []struct {
		name      string
		envVal    string // value passed to t.Setenv; "" also covers "unset" (os.Getenv returns "" either way)
		wantStage platform.Stage
		wantErr   bool
	}{
		{name: "unset defaults to development", envVal: "", wantStage: platform.StageDevelopment},
		{name: "development", envVal: "development", wantStage: platform.StageDevelopment},
		{name: "staging", envVal: "staging", wantStage: platform.StageStaging},
		{name: "production", envVal: "production", wantStage: platform.StageProduction},
		{name: "bogus", envVal: "bogus", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NARVI_STAGE", tc.envVal)

			cfg, err := platform.Load()

			if tc.wantErr {
				if err == nil {
					t.Fatalf("Load() error = nil, want error for NARVI_STAGE=%q", tc.envVal)
				}
				var invErr *platform.InvalidStageError
				if !errors.As(err, &invErr) {
					t.Fatalf("Load() error = %v, want *platform.InvalidStageError", err)
				}
				if invErr.Value != tc.envVal {
					t.Fatalf("InvalidStageError.Value = %q, want %q", invErr.Value, tc.envVal)
				}
				if cfg != nil {
					t.Fatalf("Load() cfg = %+v, want nil on error", cfg)
				}
				return
			}

			if err != nil {
				t.Fatalf("Load() error = %v, want nil", err)
			}
			if cfg == nil {
				t.Fatal("Load() cfg = nil, want non-nil on success")
			}
			if cfg.Stage != tc.wantStage {
				t.Fatalf("Load().Stage = %q, want %q", cfg.Stage, tc.wantStage)
			}
			if err := cfg.Timeouts.Validate(); err != nil {
				t.Fatalf("Load().Timeouts.Validate() = %v, want nil", err)
			}
			if cfg.LogLevel != slog.LevelInfo {
				t.Fatalf("Load().LogLevel = %v, want %v (NARVI_LOG_LEVEL unset in this test)", cfg.LogLevel, slog.LevelInfo)
			}
		})
	}
}

// TestLoadLogLevel is table-driven over NARVI_LOG_LEVEL values: unset, each
// of the four valid levels (including mixed-case, to cover the
// case-insensitive requirement), and a bogus one. Load must succeed for the
// first six and fail fast with a *platform.InvalidLogLevelError for the
// last.
func TestLoadLogLevel(t *testing.T) {
	tests := []struct {
		name      string
		envVal    string // value passed to t.Setenv; "" also covers "unset"
		wantLevel slog.Level
		wantErr   bool
	}{
		{name: "unset defaults to info", envVal: "", wantLevel: slog.LevelInfo},
		{name: "debug", envVal: "debug", wantLevel: slog.LevelDebug},
		{name: "info", envVal: "info", wantLevel: slog.LevelInfo},
		{name: "warn", envVal: "warn", wantLevel: slog.LevelWarn},
		{name: "error", envVal: "error", wantLevel: slog.LevelError},
		{name: "mixed case is accepted", envVal: "WaRn", wantLevel: slog.LevelWarn},
		{name: "bogus", envVal: "verbose", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NARVI_LOG_LEVEL", tc.envVal)

			cfg, err := platform.Load()

			if tc.wantErr {
				if err == nil {
					t.Fatalf("Load() error = nil, want error for NARVI_LOG_LEVEL=%q", tc.envVal)
				}
				var invErr *platform.InvalidLogLevelError
				if !errors.As(err, &invErr) {
					t.Fatalf("Load() error = %v, want *platform.InvalidLogLevelError", err)
				}
				if invErr.Value != tc.envVal {
					t.Fatalf("InvalidLogLevelError.Value = %q, want %q", invErr.Value, tc.envVal)
				}
				if cfg != nil {
					t.Fatalf("Load() cfg = %+v, want nil on error", cfg)
				}
				return
			}

			if err != nil {
				t.Fatalf("Load() error = %v, want nil", err)
			}
			if cfg == nil {
				t.Fatal("Load() cfg = nil, want non-nil on success")
			}
			if cfg.LogLevel != tc.wantLevel {
				t.Fatalf("Load().LogLevel = %v, want %v", cfg.LogLevel, tc.wantLevel)
			}
		})
	}
}
