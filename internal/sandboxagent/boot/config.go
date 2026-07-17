package boot

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/khazaddev/narvi/internal/domain/sandboxboot"
)

// The environment variables Load reads. bootModeEnvVar is the only one
// §6.4 explicitly pins a delivery mechanism for today ("Delivered to the
// sandbox as the NARVI_BOOT_MODE env var" -- see
// contracts/gen/go/sessionconfig's own doc comment on SessionConfig.
// BootMode); the rest are this Step's own invented plumbing, since no
// other SESSION_CONFIG delivery mechanism is pinned yet.
const (
	bootModeEnvVar     = "NARVI_BOOT_MODE"
	agentVersionEnvVar = "NARVI_AGENT_VERSION"
	imageDigestEnvVar  = "NARVI_IMAGE_DIGEST"
	workspaceDirEnvVar = "NARVI_WORKSPACE_DIR"
	logLevelEnvVar     = "NARVI_LOG_LEVEL"
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

	return Config{
		BootMode:     mode,
		AgentVersion: agentVersion,
		ImageDigest:  imageDigest,
		WorkspaceDir: workspaceDir,
		LogLevel:     logLevel,
	}, nil
}
