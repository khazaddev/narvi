package platform_test

import (
	"errors"
	"log/slog"
	"slices"
	"testing"

	"github.com/khazaddev/narvi/internal/platform"
)

// setRequiredEnv sets NARVI_STAGE, NARVI_DATABASE_URL, the three
// per-direction HMAC secret env vars (PR-06), and Step 20's ("auth v1")
// own required vars (GitHub OAuth credentials, PublicBaseURL, a valid
// 32-byte base64 token encryption key, and one allowlist mechanism) to
// valid dummy values for the duration of the calling (sub)test, via
// t.Setenv. Tests that exercise one specific, unrelated env var
// (NARVI_LOG_LEVEL, NARVI_DATABASE_URL, ...) call this first so Load
// doesn't also fail on these other required vars; tests that exercise one
// of these vars themselves call this first and then override that one var
// with the value under test.
func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("NARVI_STAGE", "development")
	t.Setenv("NARVI_DATABASE_URL", "postgres://narvi:narvi@localhost:5432/narvi_test?sslmode=disable")
	t.Setenv("NARVI_HMAC_SANDBOX_SECRET", "test-sandbox-secret")
	t.Setenv("NARVI_HMAC_BOTS_SECRET", "test-bots-secret")
	t.Setenv("NARVI_HMAC_WEBHOOK_SECRET", "test-webhook-secret")
	t.Setenv("NARVI_GITHUB_CLIENT_ID", "test-github-client-id")
	t.Setenv("NARVI_GITHUB_CLIENT_SECRET", "test-github-client-secret")
	t.Setenv("NARVI_PUBLIC_BASE_URL", "http://localhost:8080")
	t.Setenv("NARVI_TOKEN_ENCRYPTION_KEY", "MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=") // base64 of exactly 32 bytes
	t.Setenv("NARVI_ALLOWED_EMAIL_DOMAINS", "example.com")
	t.Setenv("NARVI_ALLOWED_GITHUB_ORGS", "")
	t.Setenv("NARVI_ALLOWED_EMAILS", "")
	t.Setenv("NARVI_INITIAL_ADMIN_EMAILS", "")
	t.Setenv("NARVI_MODAL_BASE_URL", "https://modal.example.test")
	t.Setenv("NARVI_MODAL_AUTH_TOKEN", "test-modal-auth-token")
	t.Setenv("NARVI_MODAL_EGRESS_PROXY_URL", "")
	t.Setenv("NARVI_OPENCODE_RUNTIME_VERSION", "")
	t.Setenv("NARVI_LINEAR_WEBHOOK_SECRET", "test-linear-webhook-secret")
	t.Setenv("NARVI_LINEAR_CLIENT_ID", "test-linear-client-id")
	t.Setenv("NARVI_LINEAR_CLIENT_SECRET", "test-linear-client-secret")
	t.Setenv("NARVI_LINEAR_DEFAULT_REPO_NAME", "narvi")
	t.Setenv("NARVI_LINEAR_DEFAULT_REPO_URL", "https://github.com/khazaddev/narvi")
}

// TestLoad is table-driven over NARVI_STAGE values: each of the three
// valid stages succeeds, an invalid one fails fast with a
// *platform.InvalidStageError, and unset fails fast with a
// *platform.MissingRequiredEnvError (NARVI_STAGE has no safe default --
// see internal/platform/config.go's own envVarName doc comment: a
// production deploy that forgets to set it must never silently boot as
// StageDevelopment, since that would weaken every auth cookie's Secure
// attribute).
func TestLoad(t *testing.T) {
	tests := []struct {
		name      string
		envVal    string // value passed to t.Setenv; "" also covers "unset" (os.Getenv returns "" either way)
		wantStage platform.Stage
		wantErr   bool
		wantUnset bool // true only for the "unset" case: expect *MissingRequiredEnvError, not *InvalidStageError
	}{
		{name: "unset fails fast", envVal: "", wantErr: true, wantUnset: true},
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
				if tc.wantUnset {
					var missErr *platform.MissingRequiredEnvError
					if !errors.As(err, &missErr) {
						t.Fatalf("Load() error = %v, want *platform.MissingRequiredEnvError", err)
					}
					if missErr.EnvVar != "NARVI_STAGE" {
						t.Fatalf("MissingRequiredEnvError.EnvVar = %q, want %q", missErr.EnvVar, "NARVI_STAGE")
					}
					if cfg != nil {
						t.Fatalf("Load() cfg = %+v, want nil on error", cfg)
					}
					return
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

// TestLoadMakeDevEnv proves config.Load() succeeds when called with
// exactly the env vars the Makefile's own `dev` target sets (including its
// NARVI_STAGE=development line, added alongside the pre-existing
// NARVI_DATABASE_URL/NARVI_HMAC_* lines) plus this codebase's own other
// required vars, supplied here via setRequiredEnv's own generic
// placeholders rather than the Makefile's real literal values --
// TestLoadMakefileDevTargetValues below is the one that asserts against the
// Makefile's own complete, real literal env-var set (GitHub OAuth, Modal,
// token encryption key, allowlist all included, since a later batch closed
// the gap this comment used to describe); this test's own narrower job is
// just confirming NARVI_STAGE itself keeps Load() passing.
func TestLoadMakeDevEnv(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("NARVI_STAGE", "development")
	t.Setenv("NARVI_DATABASE_URL", "postgres://narvi:narvi@localhost:5432/narvi?sslmode=disable")
	t.Setenv("NARVI_HMAC_SANDBOX_SECRET", "dev-only-insecure-sandbox-secret")
	t.Setenv("NARVI_HMAC_BOTS_SECRET", "dev-only-insecure-bots-secret")
	t.Setenv("NARVI_HMAC_WEBHOOK_SECRET", "dev-only-insecure-webhook-secret")

	cfg, err := platform.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil (make dev's own env vars must be sufficient)", err)
	}
	if cfg.Stage != platform.StageDevelopment {
		t.Fatalf("Load().Stage = %q, want %q", cfg.Stage, platform.StageDevelopment)
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

// TestLoadGitHubOAuthConfig is table-driven over NARVI_GITHUB_CLIENT_ID /
// NARVI_GITHUB_CLIENT_SECRET / NARVI_PUBLIC_BASE_URL, each individually
// unset: Load must fail fast with a *platform.MissingRequiredEnvError
// naming that exact env var. A final subtest confirms Load succeeds and
// threads all three values through when every one is set.
func TestLoadGitHubOAuthConfig(t *testing.T) {
	tests := []struct {
		name   string
		envVar string
	}{
		{name: "client id missing", envVar: "NARVI_GITHUB_CLIENT_ID"},
		{name: "client secret missing", envVar: "NARVI_GITHUB_CLIENT_SECRET"},
		{name: "public base url missing", envVar: "NARVI_PUBLIC_BASE_URL"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv(tc.envVar, "")

			cfg, err := platform.Load()
			if err == nil {
				t.Fatalf("Load() error = nil, want error for %s=%q", tc.envVar, "")
			}
			var missErr *platform.MissingRequiredEnvError
			if !errors.As(err, &missErr) {
				t.Fatalf("Load() error = %v, want *platform.MissingRequiredEnvError", err)
			}
			if missErr.EnvVar != tc.envVar {
				t.Fatalf("MissingRequiredEnvError.EnvVar = %q, want %q", missErr.EnvVar, tc.envVar)
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
		if cfg.GitHubClientID != "test-github-client-id" {
			t.Errorf("Load().GitHubClientID = %q, want %q", cfg.GitHubClientID, "test-github-client-id")
		}
		if cfg.GitHubClientSecret != "test-github-client-secret" {
			t.Errorf("Load().GitHubClientSecret = %q, want %q", cfg.GitHubClientSecret, "test-github-client-secret")
		}
		if cfg.PublicBaseURL != "http://localhost:8080" {
			t.Errorf("Load().PublicBaseURL = %q, want %q", cfg.PublicBaseURL, "http://localhost:8080")
		}
	})
}

// TestLoadModalConfig mirrors TestLoadGitHubOAuthConfig's own table shape:
// NARVI_MODAL_BASE_URL/NARVI_MODAL_AUTH_TOKEN are each individually
// required (Step 21, "e2e happy path" -- this Step is the SandboxProvider's
// first real production caller), NARVI_MODAL_EGRESS_PROXY_URL stays
// optional.
func TestLoadModalConfig(t *testing.T) {
	tests := []struct {
		name   string
		envVar string
	}{
		{name: "base url missing", envVar: "NARVI_MODAL_BASE_URL"},
		{name: "auth token missing", envVar: "NARVI_MODAL_AUTH_TOKEN"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv(tc.envVar, "")

			cfg, err := platform.Load()
			if err == nil {
				t.Fatalf("Load() error = nil, want error for %s=%q", tc.envVar, "")
			}
			var missErr *platform.MissingRequiredEnvError
			if !errors.As(err, &missErr) {
				t.Fatalf("Load() error = %v, want *platform.MissingRequiredEnvError", err)
			}
			if missErr.EnvVar != tc.envVar {
				t.Fatalf("MissingRequiredEnvError.EnvVar = %q, want %q", missErr.EnvVar, tc.envVar)
			}
			if cfg != nil {
				t.Fatalf("Load() cfg = %+v, want nil on error", cfg)
			}
		})
	}

	t.Run("both required set, proxy optional, succeeds", func(t *testing.T) {
		setRequiredEnv(t)

		cfg, err := platform.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil", err)
		}
		if cfg.ModalBaseURL != "https://modal.example.test" {
			t.Errorf("Load().ModalBaseURL = %q, want %q", cfg.ModalBaseURL, "https://modal.example.test")
		}
		if cfg.ModalAuthToken != "test-modal-auth-token" {
			t.Errorf("Load().ModalAuthToken = %q, want %q", cfg.ModalAuthToken, "test-modal-auth-token")
		}
		if cfg.ModalEgressProxyURL != "" {
			t.Errorf("Load().ModalEgressProxyURL = %q, want empty (unset in this test)", cfg.ModalEgressProxyURL)
		}
	})
}

// TestLoadOpenCodeRuntimeVersion covers Step 26's ("image builds") own
// optional NARVI_OPENCODE_RUNTIME_VERSION: unset defaults to
// defaultOpenCodeRuntimeVersion (pinned equal to .github/workflows/ci.yml's
// own opencode-ai pin at the time this Step was written -- see that
// constant's own doc comment for the residual, honestly-named drift risk),
// and an explicit override threads through verbatim.
func TestLoadOpenCodeRuntimeVersion(t *testing.T) {
	t.Run("unset defaults to the pinned CI version", func(t *testing.T) {
		setRequiredEnv(t)

		cfg, err := platform.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil", err)
		}
		if cfg.OpenCodeRuntimeVersion != "1.17.15" {
			t.Errorf("Load().OpenCodeRuntimeVersion = %q, want %q", cfg.OpenCodeRuntimeVersion, "1.17.15")
		}
	})

	t.Run("explicit override threads through verbatim", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_OPENCODE_RUNTIME_VERSION", "1.18.0")

		cfg, err := platform.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil", err)
		}
		if cfg.OpenCodeRuntimeVersion != "1.18.0" {
			t.Errorf("Load().OpenCodeRuntimeVersion = %q, want %q", cfg.OpenCodeRuntimeVersion, "1.18.0")
		}
	})
}

// TestLoadTokenEncryptionKey covers all three outcomes for
// NARVI_TOKEN_ENCRYPTION_KEY: unset (*platform.MissingRequiredEnvError),
// not valid base64 or the wrong decoded length (both
// *platform.InvalidTokenEncryptionKeyError), and a valid 32-byte key
// (Load succeeds and threads the DECODED bytes through, never the raw
// base64 string).
func TestLoadTokenEncryptionKey(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_TOKEN_ENCRYPTION_KEY", "")

		_, err := platform.Load()
		var missErr *platform.MissingRequiredEnvError
		if !errors.As(err, &missErr) {
			t.Fatalf("Load() error = %v, want *platform.MissingRequiredEnvError", err)
		}
		if missErr.EnvVar != "NARVI_TOKEN_ENCRYPTION_KEY" {
			t.Fatalf("MissingRequiredEnvError.EnvVar = %q, want %q", missErr.EnvVar, "NARVI_TOKEN_ENCRYPTION_KEY")
		}
	})

	t.Run("not valid base64", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_TOKEN_ENCRYPTION_KEY", "not-valid-base64!!!")

		_, err := platform.Load()
		var keyErr *platform.InvalidTokenEncryptionKeyError
		if !errors.As(err, &keyErr) {
			t.Fatalf("Load() error = %v, want *platform.InvalidTokenEncryptionKeyError", err)
		}
	})

	t.Run("wrong decoded length", func(t *testing.T) {
		setRequiredEnv(t)
		// Valid base64, but decodes to far fewer than 32 bytes.
		t.Setenv("NARVI_TOKEN_ENCRYPTION_KEY", "dG9vc2hvcnQ=")

		_, err := platform.Load()
		var keyErr *platform.InvalidTokenEncryptionKeyError
		if !errors.As(err, &keyErr) {
			t.Fatalf("Load() error = %v, want *platform.InvalidTokenEncryptionKeyError", err)
		}
	})

	t.Run("valid 32-byte key succeeds", func(t *testing.T) {
		setRequiredEnv(t)

		cfg, err := platform.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil", err)
		}
		if len(cfg.TokenEncryptionKey) != 32 {
			t.Fatalf("len(Load().TokenEncryptionKey) = %d, want 32", len(cfg.TokenEncryptionKey))
		}
	})
}

// TestLoadAllowlist proves Load fails fast with *platform.EmptyAllowlistError
// when all three allowlist env vars are empty, and otherwise parses each
// comma-separated var (trimmed, empty entries dropped) independently.
func TestLoadAllowlist(t *testing.T) {
	t.Run("all three empty fails fast", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_ALLOWED_EMAIL_DOMAINS", "")
		t.Setenv("NARVI_ALLOWED_GITHUB_ORGS", "")
		t.Setenv("NARVI_ALLOWED_EMAILS", "")

		cfg, err := platform.Load()
		if err == nil {
			t.Fatal("Load() error = nil, want error when all 3 allowlist mechanisms are empty")
		}
		var allowErr *platform.EmptyAllowlistError
		if !errors.As(err, &allowErr) {
			t.Fatalf("Load() error = %v, want *platform.EmptyAllowlistError", err)
		}
		if cfg != nil {
			t.Fatalf("Load() cfg = %+v, want nil on error", cfg)
		}
	})

	t.Run("only github orgs set succeeds", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_ALLOWED_EMAIL_DOMAINS", "")
		t.Setenv("NARVI_ALLOWED_GITHUB_ORGS", "my-org")
		t.Setenv("NARVI_ALLOWED_EMAILS", "")

		cfg, err := platform.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil", err)
		}
		if len(cfg.AllowedEmailDomains) != 0 {
			t.Errorf("Load().AllowedEmailDomains = %v, want empty", cfg.AllowedEmailDomains)
		}
		if want := []string{"my-org"}; !slices.Equal(cfg.AllowedGitHubOrgs, want) {
			t.Errorf("Load().AllowedGitHubOrgs = %v, want %v", cfg.AllowedGitHubOrgs, want)
		}
	})

	t.Run("comma-separated list is trimmed and drops empty entries", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_ALLOWED_EMAIL_DOMAINS", " example.com ,, other.com ,")
		t.Setenv("NARVI_ALLOWED_GITHUB_ORGS", "")
		t.Setenv("NARVI_ALLOWED_EMAILS", "")

		cfg, err := platform.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil", err)
		}
		want := []string{"example.com", "other.com"}
		if !slices.Equal(cfg.AllowedEmailDomains, want) {
			t.Errorf("Load().AllowedEmailDomains = %v, want %v", cfg.AllowedEmailDomains, want)
		}
	})
}

// TestLoadInitialAdminEmails proves NARVI_INITIAL_ADMIN_EMAILS is optional
// (Load succeeds when unset, with a nil/empty result) and parsed the same
// comma-separated way as the allowlist vars when set.
func TestLoadInitialAdminEmails(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_INITIAL_ADMIN_EMAILS", "")

		cfg, err := platform.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil", err)
		}
		if len(cfg.InitialAdminEmails) != 0 {
			t.Errorf("Load().InitialAdminEmails = %v, want empty", cfg.InitialAdminEmails)
		}
	})

	t.Run("set", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_INITIAL_ADMIN_EMAILS", "admin1@example.com, admin2@example.com")

		cfg, err := platform.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil", err)
		}
		want := []string{"admin1@example.com", "admin2@example.com"}
		if !slices.Equal(cfg.InitialAdminEmails, want) {
			t.Errorf("Load().InitialAdminEmails = %v, want %v", cfg.InitialAdminEmails, want)
		}
	})
}

// TestLoadMakefileDevTargetValues is the concrete proof "make dev boots
// again": it sets EXACTLY the Makefile's own `dev:` target env vars (every
// one of them, verbatim -- not setRequiredEnv's own arbitrary test dummy
// values) and asserts Load() succeeds. Before this batch, the Makefile's
// dev target only ever exported 4 of the ~12 vars Load requires
// (NARVI_DATABASE_URL + the 3 HMAC secrets), so `go run ./cmd/control-plane
// serve` invoked via `make dev` failed fast at Load() with several distinct
// *platform.MissingRequiredEnvError/*platform.EmptyAllowlistError values --
// this test fails the exact same way if the Makefile ever regresses back to
// that state (or if any one of these dev-only placeholder values stops
// being individually valid, e.g. the encryption key no longer decoding to
// 32 bytes).
func TestLoadMakefileDevTargetValues(t *testing.T) {
	t.Setenv("NARVI_DATABASE_URL", "postgres://narvi:narvi@localhost:5432/narvi?sslmode=disable")
	t.Setenv("NARVI_HMAC_SANDBOX_SECRET", "dev-only-insecure-sandbox-secret")
	t.Setenv("NARVI_HMAC_BOTS_SECRET", "dev-only-insecure-bots-secret")
	t.Setenv("NARVI_HMAC_WEBHOOK_SECRET", "dev-only-insecure-webhook-secret")
	t.Setenv("NARVI_STAGE", "development")
	t.Setenv("NARVI_GITHUB_CLIENT_ID", "dev-github-client-id-placeholder")
	t.Setenv("NARVI_GITHUB_CLIENT_SECRET", "dev-github-client-secret-placeholder")
	t.Setenv("NARVI_PUBLIC_BASE_URL", "http://localhost:8080")
	t.Setenv("NARVI_TOKEN_ENCRYPTION_KEY", "X4x5GAK5D4bwFxg5fEzToXLfPfe2XwZp8U3CR/Pl1Z4=")
	t.Setenv("NARVI_ALLOWED_GITHUB_ORGS", "dev-org-placeholder")
	t.Setenv("NARVI_MODAL_BASE_URL", "http://localhost:9999")
	t.Setenv("NARVI_MODAL_AUTH_TOKEN", "dev-modal-token-placeholder")
	t.Setenv("NARVI_LINEAR_WEBHOOK_SECRET", "dev-linear-webhook-secret-placeholder")
	t.Setenv("NARVI_LINEAR_CLIENT_ID", "dev-linear-client-id-placeholder")
	t.Setenv("NARVI_LINEAR_CLIENT_SECRET", "dev-linear-client-secret-placeholder")
	t.Setenv("NARVI_LINEAR_DEFAULT_REPO_NAME", "narvi")
	t.Setenv("NARVI_LINEAR_DEFAULT_REPO_URL", "https://github.com/khazaddev/narvi")
	// Every other allowlist/optional var is deliberately left unset here,
	// matching the Makefile's dev target exactly (it never sets
	// NARVI_ALLOWED_EMAIL_DOMAINS, NARVI_ALLOWED_EMAILS,
	// NARVI_INITIAL_ADMIN_EMAILS, NARVI_MODAL_EGRESS_PROXY_URL,
	// NARVI_HTTP_ADDR, or NARVI_LOG_LEVEL).

	cfg, err := platform.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil (these are exactly the Makefile dev target's own env vars)", err)
	}
	if cfg == nil {
		t.Fatal("Load() cfg = nil, want non-nil on success")
	}
	if len(cfg.TokenEncryptionKey) != 32 {
		t.Errorf("len(Load().TokenEncryptionKey) = %d, want 32", len(cfg.TokenEncryptionKey))
	}
	if len(cfg.AllowedGitHubOrgs) == 0 {
		t.Error("Load().AllowedGitHubOrgs is empty, want the dev-org-placeholder allowlist entry")
	}
	if cfg.ModalBaseURL != "http://localhost:9999" {
		t.Errorf("Load().ModalBaseURL = %q, want %q", cfg.ModalBaseURL, "http://localhost:9999")
	}
}
