// This file (config.go) implements typed config validated at boot,
// fail-fast, with named errors (§5.4, §11).

package platform

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// Stage identifies which deployment stage the control plane is running in.
// Typed (rather than a bare string) so an invalid value fails fast at boot
// instead of silently propagating.
//
// Named Stage, not Environment: the technical plan already reserves
// "Environment" for a distinct, load-bearing domain entity (repo/automation
// config with path scoping and secrets — §14.1, §12.2 item 5, PR_PLAN row
// 10 "domain: Environment scoping"). Reusing that name here for an unrelated
// deploy-stage concept would collide with it once PR-10 lands.
type Stage string

// The only valid Stage values. Load rejects anything else.
const (
	StageDevelopment Stage = "development"
	StageStaging     Stage = "staging"
	StageProduction  Stage = "production"
)

// envVarName is the process environment variable Load reads to select
// Stage.
const envVarName = "NARVI_STAGE"

// InvalidStageError is returned by Load when NARVI_STAGE is set to a value
// that is not one of StageDevelopment, StageStaging, or StageProduction.
type InvalidStageError struct {
	Value string
}

func (e *InvalidStageError) Error() string {
	return fmt.Sprintf("invalid %s=%q: must be one of %q, %q, %q",
		envVarName, e.Value, StageDevelopment, StageStaging, StageProduction)
}

// logLevelEnvVarName is the process environment variable Load reads to
// select LogLevel (PR-03, §5.3).
const logLevelEnvVarName = "NARVI_LOG_LEVEL"

// defaultLogLevelValue is the NARVI_LOG_LEVEL value Load assumes when the
// variable is unset, mirroring how an unset NARVI_STAGE defaults to
// StageDevelopment above.
const defaultLogLevelValue = "info"

// InvalidLogLevelError is returned by Load when NARVI_LOG_LEVEL is set to a
// value that is not (case-insensitively) one of debug, info, warn, or
// error. Named and structured the same way as InvalidStageError.
type InvalidLogLevelError struct {
	Value string
}

func (e *InvalidLogLevelError) Error() string {
	return fmt.Sprintf("invalid %s=%q: must be one of %q, %q, %q, %q (case-insensitive)",
		logLevelEnvVarName, e.Value, "debug", "info", "warn", "error")
}

// parseLogLevel validates raw (already defaulted to defaultLogLevelValue if
// the env var was unset) against the four accepted spellings, case-
// insensitively, and returns the corresponding slog.Level. Deliberately an
// explicit switch (not slog.Level.UnmarshalText) so only exactly these four
// names are accepted — UnmarshalText also accepts numeric offset suffixes
// like "error-8", which is more than this env var is specified to take.
func parseLogLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(raw) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, &InvalidLogLevelError{Value: raw}
	}
}

// databaseURLEnvVarName is the process environment variable Load reads for
// the Postgres DSN (PR-06, §1's stack-choices line: golang-migrate +
// pgx/v5). Required — no default, since there is no safe placeholder DSN.
const databaseURLEnvVarName = "NARVI_DATABASE_URL"

// httpAddrEnvVarName is the process environment variable Load reads for the
// control-plane HTTP listen address (PR-06). Optional: an unset/empty value
// defaults to defaultHTTPAddr.
const httpAddrEnvVarName = "NARVI_HTTP_ADDR"

// defaultHTTPAddr is the NARVI_HTTP_ADDR value Load assumes when the
// variable is unset, mirroring how an unset NARVI_STAGE defaults to
// StageDevelopment above.
const defaultHTTPAddr = ":8080"

// hmacSandboxSecretEnvVarName, hmacBotsSecretEnvVarName, and
// hmacWebhookSecretEnvVarName are the three per-direction HMAC secret
// env vars §5.2 requires: "Separate secrets per direction (sandbox→CP,
// CP→bots, webhook ingress) so one rotation doesn't touch everything."
// All three are required in every stage, including development — Load
// never supplies a baked-in default value for any of them.
const (
	hmacSandboxSecretEnvVarName = "NARVI_HMAC_SANDBOX_SECRET"
	hmacBotsSecretEnvVarName    = "NARVI_HMAC_BOTS_SECRET"
	hmacWebhookSecretEnvVarName = "NARVI_HMAC_WEBHOOK_SECRET"
)

// MissingRequiredEnvError is returned by Load when a required environment
// variable that has no safe default (NARVI_DATABASE_URL today) is unset or
// empty.
type MissingRequiredEnvError struct {
	EnvVar string
}

func (e *MissingRequiredEnvError) Error() string {
	return fmt.Sprintf("missing required %s (no default)", e.EnvVar)
}

// InvalidHMACSecretError is returned by Load when one of the three
// direction-specific HMAC secret env vars (§5.2) is unset or empty. A
// distinct, named error (same pattern as InvalidStageError/
// InvalidLogLevelError above) so a misconfigured deploy names exactly
// which direction's secret is missing, never a single generic "config
// invalid" — and so that, per §5.2, no direction is ever silently
// defaulted.
type InvalidHMACSecretError struct {
	EnvVar string
}

func (e *InvalidHMACSecretError) Error() string {
	return fmt.Sprintf(
		"missing required %s: every stage (including development) must set its own HMAC secret — §5.2 requires separate secrets per direction, never a baked-in default",
		e.EnvVar,
	)
}

// Config is the top-level, typed control-plane configuration, validated
// once at boot (§5.4, §11: "typed config validated at boot, fail-fast,
// named errors").
type Config struct {
	Stage    Stage
	Timeouts Timeouts

	// LogLevel is the minimum slog level the process logs at (PR-03, §5.3),
	// read from NARVI_LOG_LEVEL (debug/info/warn/error, case-insensitive,
	// default "info").
	LogLevel slog.Level

	// DatabaseURL is the Postgres DSN used by
	// adapters/outbound/postgres.NewPool and the boot-time migration run
	// (PR-06), read from NARVI_DATABASE_URL. Required; Load does not
	// validate that the DSN itself parses beyond non-empty — pgxpool.New
	// surfaces a real connection error at boot if it's malformed.
	DatabaseURL string

	// HTTPAddr is the address `narvi serve` listens on (PR-06), read from
	// NARVI_HTTP_ADDR. Optional: defaults to ":8080".
	HTTPAddr string

	// HMACSandboxSecret, HMACBotsSecret, and HMACWebhookSecret are the
	// three direction-specific secrets §5.2 requires ("Separate secrets
	// per direction (sandbox→CP, CP→bots, webhook ingress) so one
	// rotation doesn't touch everything"), read from
	// NARVI_HMAC_SANDBOX_SECRET, NARVI_HMAC_BOTS_SECRET, and
	// NARVI_HMAC_WEBHOOK_SECRET respectively. All three are required in
	// every stage, including development — never defaulted.
	HMACSandboxSecret string
	HMACBotsSecret    string
	HMACWebhookSecret string
}

// Load reads process configuration and validates it fail-fast, returning
// named, structured errors (joined via errors.Join when more than one
// check fails) instead of letting an invalid config boot silently. Callers
// (cmd/control-plane/main.go) call this once at process start.
func Load() (*Config, error) {
	stage := Stage(os.Getenv(envVarName))
	if stage == "" {
		stage = StageDevelopment
	}

	var errs []error

	switch stage {
	case StageDevelopment, StageStaging, StageProduction:
		// valid
	default:
		errs = append(errs, &InvalidStageError{Value: string(stage)})
	}

	timeouts := DefaultTimeouts()
	if err := timeouts.Validate(); err != nil {
		errs = append(errs, err)
	}

	rawLogLevel := os.Getenv(logLevelEnvVarName)
	if rawLogLevel == "" {
		rawLogLevel = defaultLogLevelValue
	}
	logLevel, err := parseLogLevel(rawLogLevel)
	if err != nil {
		errs = append(errs, err)
	}

	databaseURL := os.Getenv(databaseURLEnvVarName)
	if databaseURL == "" {
		errs = append(errs, &MissingRequiredEnvError{EnvVar: databaseURLEnvVarName})
	}

	httpAddr := os.Getenv(httpAddrEnvVarName)
	if httpAddr == "" {
		httpAddr = defaultHTTPAddr
	}

	hmacSandboxSecret := os.Getenv(hmacSandboxSecretEnvVarName)
	if hmacSandboxSecret == "" {
		errs = append(errs, &InvalidHMACSecretError{EnvVar: hmacSandboxSecretEnvVarName})
	}

	hmacBotsSecret := os.Getenv(hmacBotsSecretEnvVarName)
	if hmacBotsSecret == "" {
		errs = append(errs, &InvalidHMACSecretError{EnvVar: hmacBotsSecretEnvVarName})
	}

	hmacWebhookSecret := os.Getenv(hmacWebhookSecretEnvVarName)
	if hmacWebhookSecret == "" {
		errs = append(errs, &InvalidHMACSecretError{EnvVar: hmacWebhookSecretEnvVarName})
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	return &Config{
		Stage:             stage,
		Timeouts:          timeouts,
		LogLevel:          logLevel,
		DatabaseURL:       databaseURL,
		HTTPAddr:          httpAddr,
		HMACSandboxSecret: hmacSandboxSecret,
		HMACBotsSecret:    hmacBotsSecret,
		HMACWebhookSecret: hmacWebhookSecret,
	}, nil
}
