package platform_test

import (
	"errors"
	"log/slog"
	"slices"
	"testing"

	"github.com/khazaddev/narvi/internal/platform"
)

// setRequiredEnv sets NARVI_STAGE, NARVI_DATABASE_URL, the three
// per-direction HMAC secret env vars (PR-06), Step 20's ("auth v1") own
// required vars (GitHub OAuth credentials, PublicBaseURL, a valid 32-byte
// base64 token encryption key, and one allowlist mechanism), Step
// 32's ("GitHub ingress") own required vars (the real GitHub webhook
// secret and the bot mention handle), and Step 35's ("outbox delivery")
// own GitHub bot token to valid dummy values for the
// duration of the calling (sub)test, via
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
	t.Setenv("NARVI_GITHUB_WEBHOOK_SECRET", "test-github-webhook-secret")
	t.Setenv("NARVI_GITHUB_BOT_HANDLE", "test-bot")
	t.Setenv("NARVI_GITHUB_BOT_TOKEN", "test-github-bot-token")
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
	// Step 33 ("Slack ingress") own required vars -- SlackDefaultRepoName/
	// URL are deliberately left unset here since they're optional (see
	// internal/platform/config.go's own slackDefaultRepoNameEnvVarName doc
	// comment).
	t.Setenv("NARVI_SLACK_SIGNING_SECRET", "test-slack-signing-secret")
	t.Setenv("NARVI_SLACK_BOT_TOKEN", "test-slack-bot-token")
	// Step 36 ("intent classifier") own required vars --
	// NARVI_INTENT_CLASSIFIER_ACTIVE_SURFACES is deliberately left unset
	// here since it's optional (§18.5: every surface defaults to shadow
	// mode when unset).
	t.Setenv("NARVI_ANTHROPIC_API_KEY", "test-anthropic-api-key")
	t.Setenv("NARVI_INTENT_CLASSIFIER_PROVIDER", "anthropic")
	t.Setenv("NARVI_INTENT_CLASSIFIER_MODEL", "claude-haiku-4-5")
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

// TestLoadDBPoolMaxConns covers the optional NARVI_DB_POOL_MAX_CONNS:
// unset defaults to defaultDBPoolMaxConns (20 -- deliberately NOT pgxpool's
// own CPU-tied default, see dbPoolMaxConnsEnvVarName's own doc comment), an
// explicit positive integer threads through, and a non-integer or
// non-positive value fails fast with *platform.InvalidDBPoolMaxConnsError.
func TestLoadDBPoolMaxConns(t *testing.T) {
	t.Run("unset defaults to 20", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_DB_POOL_MAX_CONNS", "")

		cfg, err := platform.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil (this override is optional)", err)
		}
		if cfg.DBPoolMaxConns != 20 {
			t.Errorf("Load().DBPoolMaxConns = %d, want 20", cfg.DBPoolMaxConns)
		}
	})

	t.Run("explicit override threads through", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_DB_POOL_MAX_CONNS", "50")

		cfg, err := platform.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil", err)
		}
		if cfg.DBPoolMaxConns != 50 {
			t.Errorf("Load().DBPoolMaxConns = %d, want 50", cfg.DBPoolMaxConns)
		}
	})

	for _, tc := range []struct {
		name   string
		envVal string
	}{
		{name: "not an integer", envVal: "not-a-number"},
		{name: "zero", envVal: "0"},
		{name: "negative", envVal: "-5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("NARVI_DB_POOL_MAX_CONNS", tc.envVal)

			cfg, err := platform.Load()
			if err == nil {
				t.Fatalf("Load() error = nil, want error for NARVI_DB_POOL_MAX_CONNS=%q", tc.envVal)
			}
			var poolErr *platform.InvalidDBPoolMaxConnsError
			if !errors.As(err, &poolErr) {
				t.Fatalf("Load() error = %v, want *platform.InvalidDBPoolMaxConnsError", err)
			}
			if poolErr.Value != tc.envVal {
				t.Fatalf("InvalidDBPoolMaxConnsError.Value = %q, want %q", poolErr.Value, tc.envVal)
			}
			if cfg != nil {
				t.Fatalf("Load() cfg = %+v, want nil on error", cfg)
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

// TestLoadGitHubWebhookConfig mirrors TestLoadModalConfig's own table
// shape: NARVI_GITHUB_WEBHOOK_SECRET/NARVI_GITHUB_BOT_HANDLE (Step 32,
// "GitHub ingress", §8.2) are each individually required -- never
// defaulted, matching every other secret/credential this file already
// reads.
func TestLoadGitHubWebhookConfig(t *testing.T) {
	tests := []struct {
		name   string
		envVar string
	}{
		{name: "webhook secret missing", envVar: "NARVI_GITHUB_WEBHOOK_SECRET"},
		{name: "bot handle missing", envVar: "NARVI_GITHUB_BOT_HANDLE"},
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

	t.Run("both set, succeeds", func(t *testing.T) {
		setRequiredEnv(t)

		cfg, err := platform.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil", err)
		}
		if cfg.GitHubWebhookSecret != "test-github-webhook-secret" {
			t.Errorf("Load().GitHubWebhookSecret = %q, want %q", cfg.GitHubWebhookSecret, "test-github-webhook-secret")
		}
		if cfg.GitHubBotHandle != "test-bot" {
			t.Errorf("Load().GitHubBotHandle = %q, want %q", cfg.GitHubBotHandle, "test-bot")
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
	t.Setenv("NARVI_GITHUB_WEBHOOK_SECRET", "dev-only-insecure-github-webhook-secret")
	t.Setenv("NARVI_GITHUB_BOT_HANDLE", "narvi-bot")
	t.Setenv("NARVI_GITHUB_BOT_TOKEN", "dev-github-bot-token-placeholder")
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
	t.Setenv("NARVI_SLACK_SIGNING_SECRET", "dev-slack-signing-secret-placeholder")
	t.Setenv("NARVI_SLACK_BOT_TOKEN", "dev-slack-bot-token-placeholder")
	t.Setenv("NARVI_ANTHROPIC_API_KEY", "dev-anthropic-api-key-placeholder")
	t.Setenv("NARVI_INTENT_CLASSIFIER_PROVIDER", "anthropic")
	t.Setenv("NARVI_INTENT_CLASSIFIER_MODEL", "claude-haiku-4-5")
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

// TestLoadIntentClassifierConfig covers Step 36's ("intent classifier",
// §8.3/§18) own required vars (AnthropicAPIKey/IntentClassifierProvider/
// IntentClassifierModel -- each individually required, never defaulted,
// matching every other secret/credential this file already reads) and the
// optional, comma-separated IntentClassifierActiveSurfaces (§18.5: unset
// means every surface defaults to shadow mode).
func TestLoadIntentClassifierConfig(t *testing.T) {
	tests := []struct {
		name   string
		envVar string
	}{
		{name: "anthropic api key missing", envVar: "NARVI_ANTHROPIC_API_KEY"},
		{name: "provider missing", envVar: "NARVI_INTENT_CLASSIFIER_PROVIDER"},
		{name: "model missing", envVar: "NARVI_INTENT_CLASSIFIER_MODEL"},
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

	t.Run("all set, succeeds, active surfaces unset defaults to empty (every surface shadow)", func(t *testing.T) {
		setRequiredEnv(t)

		cfg, err := platform.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil", err)
		}
		if cfg.AnthropicAPIKey != "test-anthropic-api-key" {
			t.Errorf("Load().AnthropicAPIKey = %q, want %q", cfg.AnthropicAPIKey, "test-anthropic-api-key")
		}
		if cfg.IntentClassifierProvider != "anthropic" {
			t.Errorf("Load().IntentClassifierProvider = %q, want %q", cfg.IntentClassifierProvider, "anthropic")
		}
		if cfg.IntentClassifierModel != "claude-haiku-4-5" {
			t.Errorf("Load().IntentClassifierModel = %q, want %q", cfg.IntentClassifierModel, "claude-haiku-4-5")
		}
		if len(cfg.IntentClassifierActiveSurfaces) != 0 {
			t.Errorf("Load().IntentClassifierActiveSurfaces = %v, want empty", cfg.IntentClassifierActiveSurfaces)
		}
	})

	t.Run("explicit active surfaces parse as a comma-separated list", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_INTENT_CLASSIFIER_ACTIVE_SURFACES", "github, slack")

		cfg, err := platform.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil", err)
		}
		want := []string{"github", "slack"}
		if !slices.Equal(cfg.IntentClassifierActiveSurfaces, want) {
			t.Errorf("Load().IntentClassifierActiveSurfaces = %v, want %v", cfg.IntentClassifierActiveSurfaces, want)
		}
	})
}

// TestLoadGitHubImageBuildToken covers Step 42's ("warm boot: refresh pump
// + hook policy", §19.2) own platform-level GitHub credential --
// DELIBERATELY OPTIONAL, unlike every other GitHub-flavored secret this
// file reads: Load must succeed with an empty GitHubImageBuildToken when
// the env var is unset (the freshness pump/claim-time SHA resolution
// degrade gracefully on its own absence, §19.2), and must carry the real
// value through when it is set.
func TestLoadGitHubImageBuildToken(t *testing.T) {
	t.Run("unset succeeds with an empty value", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_GITHUB_IMAGE_BUILD_TOKEN", "")

		cfg, err := platform.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil (this credential is optional)", err)
		}
		if cfg.GitHubImageBuildToken != "" {
			t.Errorf("Load().GitHubImageBuildToken = %q, want empty when unset", cfg.GitHubImageBuildToken)
		}
	})

	t.Run("set carries the real value through", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_GITHUB_IMAGE_BUILD_TOKEN", "test-github-image-build-token")

		cfg, err := platform.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil", err)
		}
		if cfg.GitHubImageBuildToken != "test-github-image-build-token" {
			t.Errorf("Load().GitHubImageBuildToken = %q, want %q", cfg.GitHubImageBuildToken, "test-github-image-build-token")
		}
	})
}

// NARVI_CACHE_VOLUME_EPOCH no longer exists as a config surface (Step
// 43(c)'s third iteration removes the rotation escape hatch attempt 2
// added -- domain/imagebuild.CacheVolumeKey's own doc comment has the
// full "why"). TestLoadCacheVolumeEpoch used to live here; deleted along
// with the field and env var it tested, not left behind pointing at
// nothing.

// TestLoadGitHubReReviewLabel covers Step 46's ("review sessions", §8.2)
// own optional NARVI_GITHUB_REREVIEW_LABEL -- mirrors
// TestLoadGitHubImageBuildToken's own "unset succeeds, set carries through"
// shape exactly, except an unset value here defaults to a non-empty,
// genuinely usable label name rather than an empty string (this field's
// own doc comment: a safe, product-level default exists, unlike a
// credential with no safe placeholder).
func TestLoadGitHubReReviewLabel(t *testing.T) {
	t.Run("unset defaults to run-review", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_GITHUB_REREVIEW_LABEL", "")

		cfg, err := platform.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil (this label name is optional)", err)
		}
		if cfg.GitHubReReviewLabel != "run-review" {
			t.Errorf("Load().GitHubReReviewLabel = %q, want %q when unset", cfg.GitHubReReviewLabel, "run-review")
		}
	})

	t.Run("set carries the real value through", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_GITHUB_REREVIEW_LABEL", "please-review-again")

		cfg, err := platform.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil", err)
		}
		if cfg.GitHubReReviewLabel != "please-review-again" {
			t.Errorf("Load().GitHubReReviewLabel = %q, want %q", cfg.GitHubReReviewLabel, "please-review-again")
		}
	})
}

// TestLoadEpistemicCheckDefault covers Step 61's ("builder epistemic
// pre-action check", §20.4) own platform-wide default -- mirrors
// TestLoadObjectStorageConfig's own NARVI_OBJECT_STORE_USE_PATH_STYLE
// subtests exactly, the cited precedent for "optional boolean env var,
// off by default, InvalidXError on an unparseable value" (test-wiring
// bundle, adversarial review): before this test existed, flipping
// EpistemicCheckDefault's own default false->true in Load shipped green
// (nothing asserted the default at all), and the invalid-value branch
// (errs = append(errs, &InvalidEpistemicCheckDefaultError{...})) was never
// even constructed by any test.
func TestLoadEpistemicCheckDefault(t *testing.T) {
	t.Run("unset defaults to false (§20.4: off by default)", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_EPISTEMIC_CHECK_DEFAULT", "")

		cfg, err := platform.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil (this default is optional)", err)
		}
		if cfg.EpistemicCheckDefault {
			t.Errorf("Load().EpistemicCheckDefault = true, want false when unset")
		}
	})

	t.Run("set true carries the real value through", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_EPISTEMIC_CHECK_DEFAULT", "true")

		cfg, err := platform.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil", err)
		}
		if !cfg.EpistemicCheckDefault {
			t.Errorf("Load().EpistemicCheckDefault = false, want true")
		}
	})

	t.Run("set false explicitly carries through", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_EPISTEMIC_CHECK_DEFAULT", "false")

		cfg, err := platform.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil", err)
		}
		if cfg.EpistemicCheckDefault {
			t.Errorf("Load().EpistemicCheckDefault = true, want false")
		}
	})

	t.Run("invalid value fails", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_EPISTEMIC_CHECK_DEFAULT", "not-a-bool")

		_, err := platform.Load()
		if err == nil {
			t.Fatal("Load() error = nil, want error")
		}
		var epistemicErr *platform.InvalidEpistemicCheckDefaultError
		if !errors.As(err, &epistemicErr) {
			t.Fatalf("Load() error = %v, want *platform.InvalidEpistemicCheckDefaultError", err)
		}
	})
}

// TestLoadObjectStorageConfig covers Step 58's ("uploads, blob storage &
// the in-sandbox download_file tool", §28.7) object-storage block --
// feature-flagged on NARVI_OBJECT_STORE_ENDPOINT alone, with Region/Bucket
// becoming required only once an endpoint is set, and every other
// NARVI_OBJECT_STORE_* var left unvalidated entirely while the feature is
// off.
func TestLoadObjectStorageConfig(t *testing.T) {
	t.Run("unset endpoint leaves the feature off, no error", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_OBJECT_STORE_ENDPOINT", "")
		// Deliberately leave a stray, otherwise-invalid override set --
		// proves it is never even inspected while the feature is off.
		t.Setenv("NARVI_OBJECT_STORE_MAX_UPLOAD_BYTES", "not-a-number")

		cfg, err := platform.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil (uploads are optional; a stray override must not be validated while the endpoint is unset)", err)
		}
		if cfg.ObjectStorage != nil {
			t.Errorf("Load().ObjectStorage = %+v, want nil when NARVI_OBJECT_STORE_ENDPOINT is unset", cfg.ObjectStorage)
		}
	})

	t.Run("endpoint set without region fails", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_OBJECT_STORE_ENDPOINT", "http://localhost:9000")
		t.Setenv("NARVI_OBJECT_STORE_REGION", "")
		t.Setenv("NARVI_OBJECT_STORE_BUCKET", "narvi-uploads")

		cfg, err := platform.Load()
		if err == nil {
			t.Fatal("Load() error = nil, want error (region required once endpoint is set)")
		}
		var missErr *platform.MissingRequiredEnvError
		if !errors.As(err, &missErr) {
			t.Fatalf("Load() error = %v, want *platform.MissingRequiredEnvError", err)
		}
		if missErr.EnvVar != "NARVI_OBJECT_STORE_REGION" {
			t.Errorf("MissingRequiredEnvError.EnvVar = %q, want %q", missErr.EnvVar, "NARVI_OBJECT_STORE_REGION")
		}
		if cfg != nil {
			t.Fatalf("Load() cfg = %+v, want nil on error", cfg)
		}
	})

	t.Run("endpoint set without bucket fails", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_OBJECT_STORE_ENDPOINT", "http://localhost:9000")
		t.Setenv("NARVI_OBJECT_STORE_REGION", "us-east-1")
		t.Setenv("NARVI_OBJECT_STORE_BUCKET", "")

		_, err := platform.Load()
		if err == nil {
			t.Fatal("Load() error = nil, want error (bucket required once endpoint is set)")
		}
		var missErr *platform.MissingRequiredEnvError
		if !errors.As(err, &missErr) {
			t.Fatalf("Load() error = %v, want *platform.MissingRequiredEnvError", err)
		}
		if missErr.EnvVar != "NARVI_OBJECT_STORE_BUCKET" {
			t.Errorf("MissingRequiredEnvError.EnvVar = %q, want %q", missErr.EnvVar, "NARVI_OBJECT_STORE_BUCKET")
		}
	})

	t.Run("endpoint, region, bucket set, no credentials selects ambient IAM", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_OBJECT_STORE_ENDPOINT", "http://localhost:9000")
		t.Setenv("NARVI_OBJECT_STORE_REGION", "us-east-1")
		t.Setenv("NARVI_OBJECT_STORE_BUCKET", "narvi-uploads")
		t.Setenv("NARVI_OBJECT_STORE_ACCESS_KEY_ID", "")
		t.Setenv("NARVI_OBJECT_STORE_SECRET_ACCESS_KEY", "")

		cfg, err := platform.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil (both credentials empty selects ambient IAM, §28.7)", err)
		}
		if cfg.ObjectStorage == nil {
			t.Fatal("Load().ObjectStorage = nil, want non-nil once endpoint/region/bucket are set")
		}
		if cfg.ObjectStorage.AccessKeyID != "" || cfg.ObjectStorage.SecretAccessKey != "" {
			t.Errorf("Load().ObjectStorage credentials = (%q, %q), want both empty", cfg.ObjectStorage.AccessKeyID, cfg.ObjectStorage.SecretAccessKey)
		}
		if cfg.ObjectStorage.MaxUploadBytes != 100*1024*1024 {
			t.Errorf("Load().ObjectStorage.MaxUploadBytes = %d, want the 100 MiB default", cfg.ObjectStorage.MaxUploadBytes)
		}
		if cfg.ObjectStorage.MaxSessionUploadBytes != 1024*1024*1024 {
			t.Errorf("Load().ObjectStorage.MaxSessionUploadBytes = %d, want the 1 GiB default", cfg.ObjectStorage.MaxSessionUploadBytes)
		}
		if cfg.ObjectStorage.UsePathStyle {
			t.Errorf("Load().ObjectStorage.UsePathStyle = true, want false (default)")
		}
	})

	t.Run("exactly one of access key / secret set fails", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_OBJECT_STORE_ENDPOINT", "http://localhost:9000")
		t.Setenv("NARVI_OBJECT_STORE_REGION", "us-east-1")
		t.Setenv("NARVI_OBJECT_STORE_BUCKET", "narvi-uploads")
		t.Setenv("NARVI_OBJECT_STORE_ACCESS_KEY_ID", "only-the-id")
		t.Setenv("NARVI_OBJECT_STORE_SECRET_ACCESS_KEY", "")

		_, err := platform.Load()
		if err == nil {
			t.Fatal("Load() error = nil, want error (exactly one of access key/secret set is a misconfiguration)")
		}
		var credErr *platform.InvalidObjectStoreCredentialsError
		if !errors.As(err, &credErr) {
			t.Fatalf("Load() error = %v, want *platform.InvalidObjectStoreCredentialsError", err)
		}
	})

	t.Run("full static credentials, public endpoint, path style, and byte overrides all carry through", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_OBJECT_STORE_ENDPOINT", "http://minio.internal:9000")
		t.Setenv("NARVI_OBJECT_STORE_PUBLIC_ENDPOINT", "https://uploads.example.test")
		t.Setenv("NARVI_OBJECT_STORE_REGION", "us-east-1")
		t.Setenv("NARVI_OBJECT_STORE_BUCKET", "narvi-uploads")
		t.Setenv("NARVI_OBJECT_STORE_ACCESS_KEY_ID", "test-access-key")
		t.Setenv("NARVI_OBJECT_STORE_SECRET_ACCESS_KEY", "test-secret-key")
		t.Setenv("NARVI_OBJECT_STORE_USE_PATH_STYLE", "true")
		t.Setenv("NARVI_OBJECT_STORE_MAX_UPLOAD_BYTES", "12345")
		t.Setenv("NARVI_OBJECT_STORE_MAX_SESSION_UPLOAD_BYTES", "67890")

		cfg, err := platform.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil", err)
		}
		want := &platform.ObjectStorageConfig{
			Endpoint:              "http://minio.internal:9000",
			PublicEndpoint:        "https://uploads.example.test",
			Region:                "us-east-1",
			Bucket:                "narvi-uploads",
			AccessKeyID:           "test-access-key",
			SecretAccessKey:       "test-secret-key",
			UsePathStyle:          true,
			MaxUploadBytes:        12345,
			MaxSessionUploadBytes: 67890,
		}
		if *cfg.ObjectStorage != *want {
			t.Errorf("Load().ObjectStorage = %+v, want %+v", cfg.ObjectStorage, want)
		}
	})

	t.Run("invalid use-path-style value fails", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_OBJECT_STORE_ENDPOINT", "http://localhost:9000")
		t.Setenv("NARVI_OBJECT_STORE_REGION", "us-east-1")
		t.Setenv("NARVI_OBJECT_STORE_BUCKET", "narvi-uploads")
		t.Setenv("NARVI_OBJECT_STORE_USE_PATH_STYLE", "not-a-bool")

		_, err := platform.Load()
		if err == nil {
			t.Fatal("Load() error = nil, want error")
		}
		var pathErr *platform.InvalidObjectStoreUsePathStyleError
		if !errors.As(err, &pathErr) {
			t.Fatalf("Load() error = %v, want *platform.InvalidObjectStoreUsePathStyleError", err)
		}
	})

	t.Run("invalid max bytes overrides fail, both reported", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_OBJECT_STORE_ENDPOINT", "http://localhost:9000")
		t.Setenv("NARVI_OBJECT_STORE_REGION", "us-east-1")
		t.Setenv("NARVI_OBJECT_STORE_BUCKET", "narvi-uploads")
		t.Setenv("NARVI_OBJECT_STORE_MAX_UPLOAD_BYTES", "0")
		t.Setenv("NARVI_OBJECT_STORE_MAX_SESSION_UPLOAD_BYTES", "-5")

		_, err := platform.Load()
		if err == nil {
			t.Fatal("Load() error = nil, want error")
		}
		var maxErr *platform.InvalidObjectStoreMaxBytesError
		count := 0
		for _, e := range flattenJoinedErrors(err) {
			if errors.As(e, &maxErr) {
				count++
			}
		}
		if count != 2 {
			t.Errorf("got %d *InvalidObjectStoreMaxBytesError, want 2 (one per bad override)", count)
		}
	})
}

// flattenJoinedErrors recursively unwraps an errors.Join tree into a flat
// slice, so a test can count how many of a specific error type appear
// anywhere in it regardless of join nesting.
func flattenJoinedErrors(err error) []error {
	if err == nil {
		return nil
	}
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		return []error{err}
	}
	var out []error
	for _, e := range joined.Unwrap() {
		out = append(out, flattenJoinedErrors(e)...)
	}
	return out
}

// TestLoadCloudIdentityIssuerURL covers Step 73a's ("cloud identity: OIDC
// issuer", §27.3) own NARVI_CLOUD_IDENTITY_ISSUER_URL -- DELIBERATELY
// optional (unset means the whole cloud-identity capability is off,
// fail-closed -- see Config.CloudIdentityIssuerURL's own doc comment),
// unlike GitHubImageBuildToken/GitHubReReviewLabel above which are
// optional but never VALIDATED when present -- this field is, since a
// malformed value here breaks federation silently, cloud-side (see
// cloudIdentityIssuerURLEnvVarName's own doc comment).
func TestLoadCloudIdentityIssuerURL(t *testing.T) {
	t.Run("unset succeeds with an empty value (capability off)", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_CLOUD_IDENTITY_ISSUER_URL", "")

		cfg, err := platform.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil (this field is optional)", err)
		}
		if cfg.CloudIdentityIssuerURL != "" {
			t.Errorf("Load().CloudIdentityIssuerURL = %q, want empty when unset", cfg.CloudIdentityIssuerURL)
		}
	})

	t.Run("a well-formed https URL with no path carries through", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("NARVI_CLOUD_IDENTITY_ISSUER_URL", "https://issuer.narvi.example.test")

		cfg, err := platform.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil", err)
		}
		if cfg.CloudIdentityIssuerURL != "https://issuer.narvi.example.test" {
			t.Errorf("Load().CloudIdentityIssuerURL = %q, want %q", cfg.CloudIdentityIssuerURL, "https://issuer.narvi.example.test")
		}
	})

	invalidCases := []struct {
		name string
		val  string
	}{
		{"not a URL at all", "://not a url"},
		{"missing scheme", "issuer.narvi.example.test"},
		{"non-http(s) scheme", "ftp://issuer.narvi.example.test"},
		{"missing host", "https:///.well-known"},
		{"carries a path", "https://issuer.narvi.example.test/tenant-a"},
		{"carries a query string", "https://issuer.narvi.example.test?x=1"},
		{"carries a fragment", "https://issuer.narvi.example.test#frag"},
		// A bare trailing slash is a REAL path (url.Parse's own Path ==
		// "/") that doubles up against the fixed "/.well-known/..."
		// suffix httpapi/oidcdiscovery.go appends by plain string
		// concatenation -- see canonicalCloudIdentityIssuerURL's own doc
		// comment. This case pins the mutation the adversarial review
		// caught: restoring the old "&& parsed.Path != \"/\"" exemption
		// makes THIS subtest fail (Load() would return nil error instead
		// of *InvalidCloudIdentityIssuerURLError).
		{"a bare trailing slash doubles up against the fixed jwks_uri suffix", "http://localhost:8080/"},
	}
	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("NARVI_CLOUD_IDENTITY_ISSUER_URL", tc.val)

			_, err := platform.Load()
			if err == nil {
				t.Fatalf("Load() error = nil, want *platform.InvalidCloudIdentityIssuerURLError for %q", tc.val)
			}
			var urlErr *platform.InvalidCloudIdentityIssuerURLError
			if !errors.As(err, &urlErr) {
				t.Fatalf("Load() error = %v, want *platform.InvalidCloudIdentityIssuerURLError", err)
			}
		})
	}
}
