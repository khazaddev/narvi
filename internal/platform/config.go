// This file (config.go) implements typed config validated at boot,
// fail-fast, with named errors (§5.4, §11).

package platform

import (
	"errors"
	"fmt"
	"os"
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

// Config is the top-level, typed control-plane configuration, validated
// once at boot (§5.4, §11: "typed config validated at boot, fail-fast,
// named errors").
type Config struct {
	Stage    Stage
	Timeouts Timeouts
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

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	return &Config{Stage: stage, Timeouts: timeouts}, nil
}
