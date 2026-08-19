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

	// DockerRequired is §27.5's per-Environment "docker: required" flag
	// (Step 74). false is the ordinary, unchanged-behavior default. A
	// true value is assumed to have already passed the fail-closed
	// provider-capability check both call sites run independently
	// (httpapi.CreateSessionCore at session-creation time,
	// sessionactor.tryPlanSpawn again at dispatch time) -- this package
	// itself makes no provider-capability claim; see
	// CheckSubstrateCapabilities.
	DockerRequired bool

	// EgressPolicy is §27.6's per-Environment egress_policy (Step 74). Its
	// zero value (Mode == "") means "no policy attached to this
	// Environment" -- today's unchanged, unrestricted behavior. A non-zero
	// value is assumed to have already passed ValidateEgressPolicy;
	// nothing in this package re-validates it.
	EgressPolicy EgressPolicy
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

// Sentinel errors ValidateContractsPath can return, each naming a distinct
// reason a candidate contracts_path is rejected, wrapped by
// InvalidContractsPathError -- mirrors ValidatePathScope's own sentinel-
// error house style exactly (see that group's own doc comment above).
var (
	// ErrContractsPathEmpty means contracts_path was the empty string --
	// meaningless (httpapi.CreateSession's own defaultContractsPath is used
	// instead whenever the caller omits contractsPath entirely; an
	// explicit empty string is never that same "use the default" signal).
	ErrContractsPathEmpty = errors.New("environment: contracts_path is empty")

	// ErrContractsPathTraversal means contracts_path contains a ".." path
	// segment -- the SAME trust-boundary reasoning ErrPathTraversal
	// documents for path_scope above applies here: contracts_path is
	// assigned verbatim from the request body (httpapi.CreateSession) and
	// later reaches a real outbound GitHub API request (internal/adapters/
	// outbound/githubapi.ResolveContractsFingerprint), so a ".." segment
	// must never be allowed to reach outside the repo's own contracts
	// directory.
	ErrContractsPathTraversal = errors.New(`environment: contracts_path contains a ".." segment`)

	// ErrContractsPathInvalidChars means contracts_path contains a "?" or
	// "#" -- audit remediation (security-crosscutting lens): unlike
	// path_scope (a set of sparse-checkout glob patterns, never
	// interpolated into a URL), contracts_path is later interpolated into
	// a real GitHub Contents API request URL. Even though that adapter
	// now ALSO escapes every path segment it builds (a second, independent
	// layer of defense-in-depth), a "?" or "#" is rejected here too, at the
	// trust boundary, exactly like every other caller-controlled field this
	// package validates before it is ever persisted.
	ErrContractsPathInvalidChars = errors.New(`environment: contracts_path contains a "?" or "#"`)
)

// InvalidContractsPathError reports a candidate contracts_path
// ValidateContractsPath rejected, and why -- mirrors InvalidGlobError's own
// shape exactly.
type InvalidContractsPathError struct {
	// Path is the offending contracts_path value, verbatim.
	Path string
	// Reason is one of ErrContractsPathEmpty, ErrContractsPathTraversal, or
	// ErrContractsPathInvalidChars -- the base sentinel this error unwraps
	// to.
	Reason error
}

func (e *InvalidContractsPathError) Error() string {
	return fmt.Sprintf("environment: invalid contracts_path %q: %s", e.Path, e.Reason)
}

func (e *InvalidContractsPathError) Unwrap() error { return e.Reason }

// contractsPathInvalidChars are the characters ValidateContractsPath
// rejects outright -- see ErrContractsPathInvalidChars' own doc comment
// for why these two specifically.
const contractsPathInvalidChars = "?#"

// ValidateContractsPath validates a candidate contracts_path (Environment.
// ContractsPath, §14.3) before it is accepted at CreateSession time --
// the SAME trust-boundary precedent ValidatePathScope already established
// for path_scope, applied here since contracts_path had none at all
// before this audit remediation (unlike path_scope, which already went
// through ValidatePathScope's own ".." rejection and glob-syntax check).
// Checked, in order:
//
//  1. being the empty string (ErrContractsPathEmpty);
//  2. containing a ".." path segment (ErrContractsPathTraversal), reusing
//     hasDotDotSegment -- the SAME check ValidatePathScope uses;
//  3. containing a "?" or "#" (ErrContractsPathInvalidChars) -- either
//     character could otherwise rewrite or truncate the outbound GitHub
//     Contents API request internal/adapters/outbound/githubapi.
//     ResolveContractsFingerprint builds from this value (see that
//     adapter's own escapePathSegments doc comment for this package's
//     other, independent half of this same remediation).
//
// Unlike ValidatePathScope, ValidateContractsPath takes a single string,
// not a slice -- contracts_path is always exactly one repo-relative
// directory path, never a list of glob patterns.
func ValidateContractsPath(contractsPath string) error {
	if contractsPath == "" {
		return &InvalidContractsPathError{Path: contractsPath, Reason: ErrContractsPathEmpty}
	}
	if hasDotDotSegment(contractsPath) {
		return &InvalidContractsPathError{Path: contractsPath, Reason: ErrContractsPathTraversal}
	}
	if strings.ContainsAny(contractsPath, contractsPathInvalidChars) {
		return &InvalidContractsPathError{Path: contractsPath, Reason: ErrContractsPathInvalidChars}
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
