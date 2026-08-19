package sandboxsecret

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/khazaddev/narvi/internal/domain/providercredential"
)

// maxNameLength bounds a sandbox_secrets name. Not specified by §27.1;
// chosen generously (an ordinary env-var name is a handful of words at
// most) purely to reject pathological input before it reaches Postgres --
// mirrors this codebase's own "generous, not tuned" precedent for bounds
// with no specified figure (e.g. maxRequestBodyBytes,
// maxProviderCredentialsResponseSize).
const maxNameLength = 256

// narviReservedPrefix is the reserved env-var namespace §19.8 already
// established (NARVI_BOOT_MODE, NARVI_SESSION_CONFIG, and 6 siblings --
// internal/sandboxagent/boot/config.go's own env-var constants, all 8 of
// which start with this exact prefix) and §27.1 requires ValidateName to
// reject outright. A prefix check (rather than an enumerated list of the
// 8 CURRENT names) is deliberate and fail-closed: it also rejects any
// FUTURE NARVI_* var this codebase adds later, without ValidateName ever
// needing to change again -- exactly the reservation §19.8's own closing
// sentence asks for ("must be reserved and excluded before any
// user-settable env surface ships").
const narviReservedPrefix = "NARVI_"

// posixEnvVarNamePattern is the classic POSIX "Environment Variable Name"
// shape (IEEE Std 1003.1: "words consisting solely of uppercase letters,
// digits, and the '_'... and do not begin with a digit") -- the same
// shape every name this codebase already treats as an env var uses
// (NARVI_BOOT_MODE, ANTHROPIC_API_KEY, GOOGLE_GENERATIVE_AI_API_KEY, ...).
// Uppercase-only is a deliberate reading of "POSIX env-var shape" (§27.1),
// not an arbitrary tightening: every existing reserved name in this
// codebase (the 8 NARVI_* vars, all 5 providercredential.EnvVarNames
// entries) is uppercase, and a lowercase env var, while technically
// readable by a shell, is not the portable, conventional shape §27.1's
// own wording invokes.
var posixEnvVarNamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// Sentinel errors ValidateName returns, wrapped via fmt.Errorf(w"%w: ...")
// so a caller can branch on the REASON (errors.Is) while a human-facing
// message still names the offending value -- mirrors this codebase's own
// established named-sentinel-error precedent (e.g. platform.
// ModeMismatchError, though those are structs; these are plain sentinels
// since no structured field beyond the name itself is ever needed by a
// caller).
var (
	// ErrNameEmpty means name is the empty string.
	ErrNameEmpty = errors.New("sandboxsecret: name must not be empty")
	// ErrNameTooLong means name exceeds maxNameLength.
	ErrNameTooLong = errors.New("sandboxsecret: name too long")
	// ErrNameShape means name does not match posixEnvVarNamePattern --
	// not POSIX env-var shaped (uppercase letters/digits/underscore,
	// must not start with a digit).
	ErrNameShape = errors.New("sandboxsecret: name is not a valid POSIX environment variable name (uppercase letters, digits, underscore; must not start with a digit)")
	// ErrNameReservedNarviNamespace means name starts with
	// narviReservedPrefix ("NARVI_") -- §19.8's own reservation.
	ErrNameReservedNarviNamespace = errors.New("sandboxsecret: name is in the reserved NARVI_ namespace")
	// ErrNameReservedProviderCredential means name is exactly one of
	// providercredential.AllEnvVarNames -- already owned by Step 53's
	// provider-credential injection mechanism. §27.1's own "one owning
	// mechanism per env-var name" rule: this collision is refused, never
	// silently shadowed by whichever mechanism happens to write its env
	// entry last.
	ErrNameReservedProviderCredential = errors.New("sandboxsecret: name is already owned by provider credential injection")
)

// ValidateName reports whether name is an acceptable sandbox_secrets env-
// var name, per §27.1's own fail-closed rule: POSIX env-var shape, not in
// the reserved NARVI_* namespace, and not one of the names
// providercredential.EnvVarNames already owns. Returns nil when name is
// acceptable. Pure -- no I/O, no time.Now(), no randomness (CLAUDE.md
// §11) -- this only inspects name itself; it says nothing about whether
// name already has a row at some OTHER (scope, scopeTargetID) pair (a
// Postgres UNIQUE-index concern, not this function's).
func ValidateName(name string) error {
	if name == "" {
		return ErrNameEmpty
	}
	if len(name) > maxNameLength {
		return fmt.Errorf("%w: %d bytes (max %d)", ErrNameTooLong, len(name), maxNameLength)
	}
	if !posixEnvVarNamePattern.MatchString(name) {
		return fmt.Errorf("%w: %q", ErrNameShape, name)
	}
	if strings.HasPrefix(name, narviReservedPrefix) {
		return fmt.Errorf("%w: %q", ErrNameReservedNarviNamespace, name)
	}
	for _, reserved := range providercredential.AllEnvVarNames() {
		if name == reserved {
			return fmt.Errorf("%w: %q", ErrNameReservedProviderCredential, name)
		}
	}
	return nil
}
