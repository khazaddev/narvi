package boot

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
	"github.com/khazaddev/narvi/internal/domain/sandboxboot"
)

// The environment variables Load reads. bootModeEnvVar is the only one
// §6.4 explicitly pins a delivery mechanism for today ("Delivered to the
// sandbox as the NARVI_BOOT_MODE env var" -- see
// contracts/gen/go/sessionconfig's own doc comment on SessionConfig.
// BootMode); the rest are this Step's own invented plumbing, since no
// other SESSION_CONFIG delivery mechanism is pinned yet.
//
// sessionConfigEnvVar (NARVI_SESSION_CONFIG) is Step 15's own answer to
// that gap: an OPTIONAL env var carrying the full SESSION_CONFIG document
// as JSON. Its absence remains a fully valid, correct state (see Config.
// SessionConfig's own doc comment) -- dev/CI environments have no live
// session and are not required to set it.
const (
	bootModeEnvVar           = "NARVI_BOOT_MODE"
	agentVersionEnvVar       = "NARVI_AGENT_VERSION"
	imageDigestEnvVar        = "NARVI_IMAGE_DIGEST"
	workspaceDirEnvVar       = "NARVI_WORKSPACE_DIR"
	logLevelEnvVar           = "NARVI_LOG_LEVEL"
	sessionConfigEnvVar      = "NARVI_SESSION_CONFIG"
	credentialCacheDirEnvVar = "NARVI_CREDENTIAL_CACHE_DIR"
	sandboxIDEnvVar          = "NARVI_SANDBOX_ID"
)

// Defaults for every optional env var above.
const (
	// defaultAgentVersion is used when NARVI_AGENT_VERSION is unset: no
	// build-time version-stamping pipeline exists yet. A later ops/release
	// Step is expected to inject a real value via -ldflags or this env
	// var.
	defaultAgentVersion = "dev"

	// defaultImageDigest is used when NARVI_IMAGE_DIGEST is unset. HONEST
	// GAP: as of Step 12, ports.CreateSpec/the Modal adapter have no
	// mechanism to inject an arbitrary env var like this one into a
	// spawned sandbox, so in practice this will always default to
	// "unknown" until some later Step closes that gap.
	defaultImageDigest = "unknown"

	// defaultWorkspaceDir is used when NARVI_WORKSPACE_DIR is unset (§6.4:
	// "Multi-repo ordered clones under /workspace/{name}").
	defaultWorkspaceDir = "/workspace"

	// defaultLogLevelValue is used when NARVI_LOG_LEVEL is unset.
	defaultLogLevelValue = "info"

	// defaultCredentialCacheDir is used when NARVI_CREDENTIAL_CACHE_DIR is
	// unset. Deliberately OUTSIDE defaultWorkspaceDir (the agent-visible
	// /workspace tree, §6.4) so a coding agent operating there never sees
	// the raw credential cache file (§5.2).
	defaultCredentialCacheDir = "/tmp/narvi-credentials"

	// defaultSandboxID is used when NARVI_SANDBOX_ID is unset. HONEST GAP,
	// same shape as defaultImageDigest above: no Step yet wires a real
	// provider-assigned sandbox-instance id into the sandbox's own
	// environment, so internal/sandboxagent/wsbridge's X-Sandbox-ID header
	// value will always default to "" in practice until some later Step
	// closes that gap.
	defaultSandboxID = ""
)

// Config is sandbox-agent's own typed, boot-time-validated configuration,
// following the same fail-fast named-error convention as
// platform/config.go.
type Config struct {
	BootMode     sandboxboot.BootMode
	AgentVersion string
	ImageDigest  string
	WorkspaceDir string
	LogLevel     slog.Level

	// CredentialCacheDir is where the git credential helper
	// (internal/sandboxagent/credentials) caches minted credentials on
	// disk (§5.2: "caches to disk with flock"). Deliberately OUTSIDE
	// WorkspaceDir -- see defaultCredentialCacheDir's own doc comment.
	CredentialCacheDir string

	// SandboxID is the value internal/sandboxagent/wsbridge.New sends as
	// the sandbox WS connection's X-Sandbox-ID header (§6.1). HONEST GAP --
	// see defaultSandboxID's own doc comment: this defaults to "" until
	// some later Step wires a real provider-assigned sandbox-instance id
	// into the sandbox's environment.
	SandboxID string

	// SessionConfig is the full SESSION_CONFIG document (§6.4), parsed
	// from NARVI_SESSION_CONFIG when present -- nil when that env var is
	// unset. This is intentionally NOT a forced requirement: dev/CI
	// environments have no live session, and nil here (with a nil Repos
	// slice, exactly today's boot behavior) remains a fully valid,
	// correct state. When present, its own BootMode field is
	// cross-checked against the separately-read NARVI_BOOT_MODE (see
	// ModeMismatchError) -- a real, fail-fast reconciliation, the
	// same shape as ports.CreateSpec.Validate's Gen/SessionConfig.Gen
	// check.
	SessionConfig *sessionconfig.SessionConfig
}

// ModeMismatchError is returned by Load when NARVI_SESSION_CONFIG is
// present but its own bootMode field disagrees with the separately-read
// NARVI_BOOT_MODE env var. The two are a deliberate duplicate (NARVI_
// BOOT_MODE is §6.4's own pinned delivery mechanism; SessionConfig.
// BootMode travels inside the larger, optional SESSION_CONFIG document)
// with nothing structurally keeping them in sync -- a caller-side bug
// that sets one and forgets the other must be caught before either value
// is trusted, exactly like ports.CreateSpec.Validate's GenMismatchError
// catches a diverging Gen/SessionConfig.Gen pair.
type ModeMismatchError struct {
	EnvValue           string
	SessionConfigValue string
}

func (e *ModeMismatchError) Error() string {
	return fmt.Sprintf(
		"boot: %s=%q does not match NARVI_SESSION_CONFIG's bootMode=%q",
		bootModeEnvVar, e.EnvValue, e.SessionConfigValue,
	)
}

// InvalidLogLevelError is returned by Load when NARVI_LOG_LEVEL is set to
// a value that is not (case-insensitively) one of debug, info, warn, or
// error. Same shape and reasoning as platform/config.go's own
// InvalidLogLevelError -- deliberately re-implemented rather than shared,
// since sandbox-agent's config is otherwise entirely disjoint from
// control-plane's.
type InvalidLogLevelError struct {
	Value string
}

func (e *InvalidLogLevelError) Error() string {
	return fmt.Sprintf("boot: invalid %s=%q: must be one of %q, %q, %q, %q (case-insensitive)",
		logLevelEnvVar, e.Value, "debug", "info", "warn", "error")
}

// parseLogLevel validates raw (already defaulted to defaultLogLevelValue
// if the env var was unset) against the four accepted spellings,
// case-insensitively -- an explicit switch, not slog.Level.UnmarshalText,
// for the same reason platform/config.go's own parseLogLevel uses one:
// UnmarshalText also accepts numeric offset suffixes like "error-8", more
// than this env var is specified to take.
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

// Load reads and validates sandbox-agent's process configuration,
// fail-fast, from environment variables. NARVI_BOOT_MODE is required (§6.4,
// explicit); everything else is optional with a documented default.
func Load() (Config, error) {
	mode, err := sandboxboot.ParseBootMode(os.Getenv(bootModeEnvVar))
	if err != nil {
		return Config{}, fmt.Errorf("boot: %s: %w", bootModeEnvVar, err)
	}

	agentVersion := os.Getenv(agentVersionEnvVar)
	if agentVersion == "" {
		agentVersion = defaultAgentVersion
	}

	imageDigest := os.Getenv(imageDigestEnvVar)
	if imageDigest == "" {
		imageDigest = defaultImageDigest
	}

	workspaceDir := os.Getenv(workspaceDirEnvVar)
	if workspaceDir == "" {
		workspaceDir = defaultWorkspaceDir
	}

	rawLogLevel := os.Getenv(logLevelEnvVar)
	if rawLogLevel == "" {
		rawLogLevel = defaultLogLevelValue
	}
	logLevel, err := parseLogLevel(rawLogLevel)
	if err != nil {
		return Config{}, err
	}

	credentialCacheDir := os.Getenv(credentialCacheDirEnvVar)
	if credentialCacheDir == "" {
		credentialCacheDir = defaultCredentialCacheDir
	}

	sandboxID := os.Getenv(sandboxIDEnvVar)
	if sandboxID == "" {
		sandboxID = defaultSandboxID
	}

	sessionConfig, err := loadSessionConfig(mode)
	if err != nil {
		return Config{}, err
	}

	return Config{
		BootMode:           mode,
		AgentVersion:       agentVersion,
		ImageDigest:        imageDigest,
		WorkspaceDir:       workspaceDir,
		LogLevel:           logLevel,
		CredentialCacheDir: credentialCacheDir,
		SandboxID:          sandboxID,
		SessionConfig:      sessionConfig,
	}, nil
}

// loadSessionConfig reads and parses NARVI_SESSION_CONFIG when present,
// cross-checking its bootMode field against mode (the already-resolved
// NARVI_BOOT_MODE value). Returns (nil, nil) when the env var is unset --
// a fully valid, correct state (see Config.SessionConfig's own doc
// comment). A malformed JSON document is a wrapped, propagated error
// (fail-fast, matching every other Load() failure mode); a document
// missing a required SessionConfig field surfaces the generated
// UnmarshalJSON's own error UNWRAPPED and un-rehashed, per this Step's own
// scope (SessionConfig's generated json.Unmarshal already validates every
// required field is present).
func loadSessionConfig(mode sandboxboot.BootMode) (*sessionconfig.SessionConfig, error) {
	raw := os.Getenv(sessionConfigEnvVar)
	if raw == "" {
		return nil, nil
	}

	var sc sessionconfig.SessionConfig
	if err := json.Unmarshal([]byte(raw), &sc); err != nil {
		return nil, fmt.Errorf("boot: %s: %w", sessionConfigEnvVar, err)
	}

	if string(sc.BootMode) != string(mode) {
		return nil, &ModeMismatchError{
			EnvValue:           string(mode),
			SessionConfigValue: string(sc.BootMode),
		}
	}

	return &sc, nil
}
