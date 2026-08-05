package automation

import (
	"errors"
	"fmt"
)

// EnvVar is one entry of §8.4's own "per-automation env vars" -- PLAIN
// configuration threaded into every run this automation fans out (app/
// automation's own fanout.go), NEVER a secret. See this package's own
// doc.go for the explicit, documented decision to defer per-automation
// SECRETS to Step 53 -- EnvVar is that decision's own opposite case: data
// this package DOES implement, precisely because it carries no
// confidentiality requirement at all (a feature-flag name, a target
// environment label, a non-sensitive tuning parameter).
type EnvVar struct {
	// Name is the environment variable's own name -- validated by
	// ValidateEnvVars against envVarNamePattern below.
	Name string
	// Value is the environment variable's own value -- unvalidated beyond
	// "present" (an empty string is a legitimate value for some env vars,
	// e.g. an intentionally-blank feature flag).
	Value string
}

// MaxEnvVars is this package's own explicit cap on how many EnvVar entries
// a single automation may carry -- mirrors MaxFanOutTargets' own identical
// "an explicit, small, application-enforced cap on an otherwise-unbounded
// JSONB array" precedent (target.go).
const MaxEnvVars = 50

// Sentinel errors ValidateEnvVars can return -- mirrors this codebase's own
// established sentinel-error house style (internal/domain/environment's
// ErrEmptyPattern et al.) rather than a bare fmt.Errorf string.
var (
	// ErrTooManyEnvVars means the candidate list exceeds MaxEnvVars.
	ErrTooManyEnvVars = errors.New("automation: too many env vars")
	// ErrEmptyEnvVarName means an EnvVar's own Name was the empty string.
	ErrEmptyEnvVarName = errors.New("automation: env var name must not be empty")
	// ErrInvalidEnvVarName means an EnvVar's own Name failed
	// envVarNamePattern -- not a syntactically legal POSIX shell/env
	// variable name.
	ErrInvalidEnvVarName = errors.New("automation: env var name is not a valid identifier")
	// ErrDuplicateEnvVarName means the same Name appeared more than once in
	// one candidate list -- ambiguous (which value would actually apply to
	// the dispatched turn's own environment), so rejected outright rather
	// than silently letting the last one win.
	ErrDuplicateEnvVarName = errors.New("automation: duplicate env var name")
)

// InvalidEnvVarError reports a single candidate EnvVar ValidateEnvVars
// rejected, and why -- mirrors environment.InvalidGlobError's own shape
// exactly.
type InvalidEnvVarError struct {
	// Name is the offending EnvVar's own Name, verbatim.
	Name string
	// Reason is one of ErrEmptyEnvVarName, ErrInvalidEnvVarName, or
	// ErrDuplicateEnvVarName -- the base sentinel this error unwraps to.
	Reason error
}

func (e *InvalidEnvVarError) Error() string {
	return fmt.Sprintf("automation: invalid env var %q: %s", e.Name, e.Reason)
}

func (e *InvalidEnvVarError) Unwrap() error { return e.Reason }

// isValidEnvVarName reports whether name is a syntactically legal POSIX
// shell/environment variable name: one or more ASCII letters/digits/
// underscore, not starting with a digit -- the same restriction every
// POSIX shell itself enforces on `export NAME=value`, so a name this
// function accepts is guaranteed to survive being written into the
// dispatched turn's own process environment without special-character
// escaping concerns.
func isValidEnvVarName(name string) bool {
	for i, r := range name {
		switch {
		case r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			continue
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
			continue
		default:
			return false
		}
	}
	return true
}

// ValidateEnvVars validates a candidate []EnvVar list before it is accepted
// onto an automation, at creation/update time: at most MaxEnvVars entries,
// each with a non-empty, syntactically valid Name, and no two entries
// sharing the same Name. Returns the first problem found (and stops) --
// same "first error wins, no accumulation" convention as
// environment.ValidatePathScope.
func ValidateEnvVars(vars []EnvVar) error {
	if len(vars) > MaxEnvVars {
		return ErrTooManyEnvVars
	}
	seen := make(map[string]struct{}, len(vars))
	for _, v := range vars {
		if v.Name == "" {
			return &InvalidEnvVarError{Name: v.Name, Reason: ErrEmptyEnvVarName}
		}
		if !isValidEnvVarName(v.Name) {
			return &InvalidEnvVarError{Name: v.Name, Reason: ErrInvalidEnvVarName}
		}
		if _, dup := seen[v.Name]; dup {
			return &InvalidEnvVarError{Name: v.Name, Reason: ErrDuplicateEnvVarName}
		}
		seen[v.Name] = struct{}{}
	}
	return nil
}
