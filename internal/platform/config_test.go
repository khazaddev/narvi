package platform_test

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/khazaddev/narvi/internal/platform"
)

// setRequiredEnv sets NARVI_DATABASE_URL and the three per-direction HMAC
// secret env vars (PR-06) to valid dummy values for the duration of the
// calling (sub)test, via t.Setenv. Tests that exercise one specific,
// unrelated env var (NARVI_STAGE, NARVI_LOG_LEVEL, ...) call this first so
// Load doesn't also fail on these newer required vars; tests that exercise
// one of these vars themselves call this first and then override that one
// var with the value under test.
func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("NARVI_DATABASE_URL", "postgres://narvi:narvi@localhost:5432/narvi_test?sslmode=disable")
	t.Setenv("NARVI_HMAC_SANDBOX_SECRET", "test-sandbox-secret")
	t.Setenv("NARVI_HMAC_BOTS_SECRET", "test-bots-secret")
	t.Setenv("NARVI_HMAC_WEBHOOK_SECRET", "test-webhook-secret")
}

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
			setRequiredEnv(t)
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
			setRequiredEnv(t)
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

// TestLoadDatabaseURL is table-driven over NARVI_DATABASE_URL: set (Load
// succeeds and threads the value through) and unset (Load fails fast with a
// *platform.MissingRequiredEnvError naming the env var — §5.2-adjacent
// "fail-fast if empty", no DSN-parses validation attempted here).
func TestLoadDatabaseURL(t *testing.T) {
	tests := []struct {
		name    string
		envVal  string
		wantErr bool
	}{
		{name: "set", envVal: "postgres://narvi:narvi@localhost:5432/narvi?sslmode=disable"},
		{name: "unset", envVal: "", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("NARVI_DATABASE_URL", tc.envVal)

			cfg, err := platform.Load()

			if tc.wantErr {
				if err == nil {
					t.Fatalf("Load() error = nil, want error for NARVI_DATABASE_URL=%q", tc.envVal)
				}
				var missErr *platform.MissingRequiredEnvError
				if !errors.As(err, &missErr) {
					t.Fatalf("Load() error = %v, want *platform.MissingRequiredEnvError", err)
				}
				if missErr.EnvVar != "NARVI_DATABASE_URL" {
					t.Fatalf("MissingRequiredEnvError.EnvVar = %q, want %q", missErr.EnvVar, "NARVI_DATABASE_URL")
				}
				if cfg != nil {
					t.Fatalf("Load() cfg = %+v, want nil on error", cfg)
				}
				return
			}

			if err != nil {
				t.Fatalf("Load() error = %v, want nil", err)
			}
			if cfg.DatabaseURL != tc.envVal {
				t.Fatalf("Load().DatabaseURL = %q, want %q", cfg.DatabaseURL, tc.envVal)
			}
		})
	}
}

// TestLoadHTTPAddr is table-driven over NARVI_HTTP_ADDR: unset (defaults to
// ":8080") and set (threaded through as-is). Unlike DatabaseURL, this is
// optional -- Load must succeed either way.
func TestLoadHTTPAddr(t *testing.T) {
	tests := []struct {
		name   string
		envVal string
		want   string
	}{
		{name: "unset defaults to :8080", envVal: "", want: ":8080"},
		{name: "set", envVal: ":9090", want: ":9090"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("NARVI_HTTP_ADDR", tc.envVal)

			cfg, err := platform.Load()
			if err != nil {
				t.Fatalf("Load() error = %v, want nil", err)
			}
			if cfg.HTTPAddr != tc.want {
				t.Fatalf("Load().HTTPAddr = %q, want %q", cfg.HTTPAddr, tc.want)
			}
		})
	}
}

// TestLoadHMACSecrets is table-driven over each of the three per-direction
// HMAC secret env vars, individually unset: Load must fail fast with a
// *platform.InvalidHMACSecretError naming that exact env var (not a
// generic error, and not silently defaulted for any direction -- §5.2). A
// final subtest confirms Load succeeds and threads all three values through
// when every direction's secret is set.
func TestLoadHMACSecrets(t *testing.T) {
	tests := []struct {
		name   string
		envVar string
	}{
		{name: "sandbox secret missing", envVar: "NARVI_HMAC_SANDBOX_SECRET"},
		{name: "bots secret missing", envVar: "NARVI_HMAC_BOTS_SECRET"},
		{name: "webhook secret missing", envVar: "NARVI_HMAC_WEBHOOK_SECRET"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv(tc.envVar, "")

			cfg, err := platform.Load()
			if err == nil {
				t.Fatalf("Load() error = nil, want error for %s=%q", tc.envVar, "")
			}
			var hmacErr *platform.InvalidHMACSecretError
			if !errors.As(err, &hmacErr) {
				t.Fatalf("Load() error = %v, want *platform.InvalidHMACSecretError", err)
			}
			if hmacErr.EnvVar != tc.envVar {
				t.Fatalf("InvalidHMACSecretError.EnvVar = %q, want %q", hmacErr.EnvVar, tc.envVar)
			}
			if cfg != nil {
				t.Fatalf("Load() cfg = %+v, want nil on error", cfg)
			}
		})
	}

	t.Run("all three set succeeds", func(t *testing.T) {
		setRequiredEnv(t)

		cfg, err := platform.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil", err)
		}
		if cfg.HMACSandboxSecret != "test-sandbox-secret" {
			t.Fatalf("Load().HMACSandboxSecret = %q, want %q", cfg.HMACSandboxSecret, "test-sandbox-secret")
		}
		if cfg.HMACBotsSecret != "test-bots-secret" {
			t.Fatalf("Load().HMACBotsSecret = %q, want %q", cfg.HMACBotsSecret, "test-bots-secret")
		}
		if cfg.HMACWebhookSecret != "test-webhook-secret" {
			t.Fatalf("Load().HMACWebhookSecret = %q, want %q", cfg.HMACWebhookSecret, "test-webhook-secret")
		}
	})
}

// TestLoadHMACSecretsAllMissing proves Load reports all three missing
// direction secrets together (via errors.Join), not just the first one it
// happens to check.
func TestLoadHMACSecretsAllMissing(t *testing.T) {
	t.Setenv("NARVI_DATABASE_URL", "postgres://narvi:narvi@localhost:5432/narvi_test?sslmode=disable")
	t.Setenv("NARVI_HMAC_SANDBOX_SECRET", "")
	t.Setenv("NARVI_HMAC_BOTS_SECRET", "")
	t.Setenv("NARVI_HMAC_WEBHOOK_SECRET", "")

	_, err := platform.Load()
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}

	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("Load() error %v does not support errors.Join unwrapping", err)
	}

	gotEnvVars := map[string]bool{}
	for _, e := range joined.Unwrap() {
		var hmacErr *platform.InvalidHMACSecretError
		if errors.As(e, &hmacErr) {
			gotEnvVars[hmacErr.EnvVar] = true
		}
	}

	for _, want := range []string{
		"NARVI_HMAC_SANDBOX_SECRET",
		"NARVI_HMAC_BOTS_SECRET",
		"NARVI_HMAC_WEBHOOK_SECRET",
	} {
		if !gotEnvVars[want] {
			t.Errorf("Load() did not report missing %q; got: %v", want, gotEnvVars)
		}
	}
	if len(gotEnvVars) != 3 {
		t.Errorf("Load() reported %d distinct missing HMAC secrets, want exactly 3 (got: %v)", len(gotEnvVars), gotEnvVars)
	}
}
