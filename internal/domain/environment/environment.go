package environment

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// Environment is the subset of the Environment record (§14.1: "Extend the
// Environment record (referenced from session creation and automation
// targets, §3.5) with an optional path_scope: []glob ... and an optional
// mock_config") that this package's derivation functions operate over.
type Environment struct {
	// PathScope is the set of sparse-checkout glob patterns restricting
	// what materializes on the sandbox filesystem. Nil or empty means
	// full access, unchanged behavior -- §14.1's explicit default. A
	// non-nil, non-empty PathScope is assumed to have already passed
	// ValidatePathScope; nothing in this package re-validates it.
	PathScope []string

	// MockConfigured records whether a mock_config is attached to this
	// Environment (§14.3: "The mock is a versioned repo artifact... never
	// something an agent invents per session"). An Environment can be
	// path-scoped without being mock-configured, or vice versa -- the two
	// are independent optional attributes (§14.1: "an optional path_scope
	// ... and an optional mock_config"), not a package deal.
	MockConfigured bool

	// ContractsPath is the repo-relative path to the contract-driven mock
	// spec directory this Environment's sessions check for drift against
	// (§14.3: "a shared contracts/api/*.{yaml,json} spec... drives a
	// generated mock server"). Empty when MockConfigured is false --
	// there is nothing to point at without a mock_config attached in the
	// first place. When MockConfigured is true, this is either the
	// caller's own explicit path or the literal "contracts/api" default
	// (httpapi.CreateSession's own resolution, see its doc comment);
	// app/sessionactor/contractdrift.go's own checkContractDrift is the
	// one real reader.
	ContractsPath string
}

// Sentinel errors ValidatePathScope can return, each naming a distinct
// reason a candidate path_scope pattern is rejected, wrapped by
// InvalidGlobError so callers/tests can tell them apart via errors.Is
// while still getting the offending pattern via errors.As -- matching the
// sentinel-error house style used in internal/domain/{sandbox,turn,
// gitstate}.
var (
	// ErrEmptyPattern means a path_scope entry was the empty string --
	// meaningless, almost certainly a configuration mistake.
	ErrEmptyPattern = errors.New("environment: empty path_scope pattern")

	// ErrPathTraversal means a path_scope entry contains a ".." path
	// segment. path_scope is a trust boundary (§14.1: "this cannot be
	// bypassed") and a ".." segment could let a scope pattern reach
	// outside the intended sparse-checkout root, so it is rejected
	// outright rather than relied on to resolve harmlessly.
	ErrPathTraversal = errors.New(`environment: path_scope pattern contains a ".." segment`)

	// ErrInvalidGlobSyntax means a path_scope entry is not a syntactically
	// valid glob per path.Match.
	ErrInvalidGlobSyntax = errors.New("environment: path_scope pattern is not a valid glob")
)

// InvalidGlobError reports a single path_scope pattern ValidatePathScope
// rejected, and why.
type InvalidGlobError struct {
	// Pattern is the offending path_scope entry, verbatim.
	Pattern string
	// Reason is one of ErrEmptyPattern, ErrPathTraversal, or
	// ErrInvalidGlobSyntax -- the base sentinel this error unwraps to.
	Reason error
}

func (e *InvalidGlobError) Error() string {
	return fmt.Sprintf("environment: invalid path_scope pattern %q: %s", e.Pattern, e.Reason)
}

func (e *InvalidGlobError) Unwrap() error { return e.Reason }

// globSyntaxProbe is the arbitrary test string passed to path.Match when
// ValidatePathScope checks a pattern's syntax. Its content is irrelevant --
// path.Match's result (matched or not) is never consulted, only whether
// parsing the pattern itself produced path.ErrBadPattern.
const globSyntaxProbe = "environment-path-scope-glob-syntax-probe"

// hasDotDotSegment reports whether pattern contains ".." as a full
// path segment (split on "/"), not merely as a substring -- so a
// legitimate filename like "foo..bar" is never rejected, only an actual
// ".." segment (e.g. "..", "../etc", "apps/../etc").
//
// A single leading "!" is stripped before splitting: "!" is gitignore-style
// patterns' own negation sigil (a pattern beginning with "!" re-includes
// rather than excludes), not an ordinary filename character the way the
// dots in "foo..bar" are. Left unstripped, "!.." would split into the
// single token "!..", which is not string-equal to "..", letting a
// traversal segment slide past the exact-match check behind a one-
// character prefix -- exactly the shape this check exists to catch.
func hasDotDotSegment(pattern string) bool {
	pattern = strings.TrimPrefix(pattern, "!")
	for _, segment := range strings.Split(pattern, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

// ValidatePathScope validates a candidate path_scope before it is accepted
// onto an Environment (§14.1). A nil or empty patterns slice is valid --
// it means "unscoped", per §14.1's own default -- so ValidatePathScope
// only ever rejects individual bad PATTERNS within a non-empty slice,
// never the absence of a slice. Each pattern is checked, in order, for:
//
//  1. being the empty string (ErrEmptyPattern);
//  2. containing a ".." path segment (ErrPathTraversal);
//  3. failing Go's own glob-syntax validity check, via path.Match
//     (ErrInvalidGlobSyntax).
//
// A leading "/" is explicitly NOT rejected -- that is the normal,
// expected anchoring form for a sparse-checkout cone pattern (e.g.
// "/apps/web/*").
//
// ValidatePathScope returns the first invalid pattern's error (wrapped as
// *InvalidGlobError) and stops; it does not accumulate every problem in
// the slice.
func ValidatePathScope(patterns []string) error {
	for _, p := range patterns {
		if p == "" {
			return &InvalidGlobError{Pattern: p, Reason: ErrEmptyPattern}
		}
		if hasDotDotSegment(p) {
			return &InvalidGlobError{Pattern: p, Reason: ErrPathTraversal}
		}
		if _, err := path.Match(p, globSyntaxProbe); errors.Is(err, path.ErrBadPattern) {
			return &InvalidGlobError{Pattern: p, Reason: ErrInvalidGlobSyntax}
		}
	}
	return nil
}

// IsScoped reports whether env restricts what is checked out -- true iff
// len(env.PathScope) > 0. This is the single decision point for "does
// this Environment restrict what's checked out" (§14.1: "absent = full
// access, unchanged behavior").
func IsScoped(env Environment) bool {
	return len(env.PathScope) > 0
}

// SparseCheckoutPatterns returns the exact pattern list a caller (the
// clone step, §3.4/§14.1) should pass to `git sparse-checkout set
// <globs>`. For an unscoped Environment it returns nil -- the caller's job
// is then to skip running sparse-checkout entirely, not to run it with an
// empty pattern list, which has different git semantics than never
// invoking it at all.
//
// SparseCheckoutPatterns performs no validation of its own; it assumes
// env was already constructed through a path that called
// ValidatePathScope. It is a pure accessor, not a second validation pass.
func SparseCheckoutPatterns(env Environment) []string {
	if !IsScoped(env) {
		return nil
	}
	return env.PathScope
}

// RequiresProvenanceTag reports whether sessions created under env must
// carry the provenance tag §14.1 describes: "Sessions created under a
// scoped Environment carry a provenance tag (alongside the existing
// spawn_source) so the label automation and the handoff sentinel (§14.4)
// can act on it without re-deriving intent."
//
// This computes exactly IsScoped's value, and deliberately calls IsScoped
// rather than re-deriving it: the two functions exist as distinct names
// because they document two different caller-facing concepts -- IsScoped
// is a git-layer sparse-checkout decision (clone-step caller, Step 29),
// while RequiresProvenanceTag is a session-creation provenance decision
// (session-creation caller, a later Step) -- that presently happen to
// coincide but belong to different call sites reading correctly on their
// own.
func RequiresProvenanceTag(env Environment) bool {
	return IsScoped(env)
}
