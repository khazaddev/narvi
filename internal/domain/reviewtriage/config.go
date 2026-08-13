package reviewtriage

// Mode is the per-repo reviewDepth.mode override (§26.3: "reviewDepth:
// {mode: auto|always_light|always_deep, deepPaths: [...]}"). The Go zero
// value ("") is deliberately not one of the three named members --
// resolveMode below (decide.go) treats it, and any other unrecognized
// value, identically to ModeAuto (this package's own established
// default), never as a silent, accidental always_deep/always_light
// override -- a garbled config value must never itself change routing
// behavior in the more expensive OR the less safe direction.
type Mode string

const (
	// ModeAuto is the default: the v1 rule cascade (decide.go) decides.
	ModeAuto Mode = "auto"
	// ModeAlwaysLight forces every review on this repo to the light path,
	// regardless of signals -- an explicit admin override, never a
	// silent default (see Mode's own doc comment for the zero-value
	// distinction).
	ModeAlwaysLight Mode = "always_light"
	// ModeAlwaysDeep forces every review on this repo to the deep path,
	// regardless of signals.
	ModeAlwaysDeep Mode = "always_deep"
)

// Config is Decide's own per-repo-tunable half (§26.3's own named shape,
// verbatim: "{mode, deepPaths}"). See doc.go's own "Per-repo config"
// section for why the 600-line/3-root thresholds are NOT a further field
// here.
type Config struct {
	// Mode overrides the v1 rule cascade outright when set to
	// ModeAlwaysLight/ModeAlwaysDeep -- see resolveMode (decide.go).
	Mode Mode
	// DeepPaths is a repo-specific list of ADDITIONAL glob patterns that
	// route deep, layered on top of (never replacing) the fixed
	// migrations/auth/infra-as-code/CI-workflow sensitive-glob set
	// (sensitiveglob.go) -- e.g. a repo's own billing or payments
	// directory, which touches nothing internal/domain/autoapproval's
	// own built-in classifier recognizes as sensitive by name alone. See
	// matchDeepPath (decide.go) for the two supported pattern shapes
	// (a bare directory/file prefix, or a single-`*`-wildcard glob).
	DeepPaths []string
}

// DefaultConfig is the engine's own built-in default -- applied whenever
// a repo has never configured reviewDepth at all (internal/app/
// reviewtriage.LoadConfig's own doc comment: a missing repo_settings row,
// or a NULL reviewDepth column on an existing one, both resolve to this).
func DefaultConfig() Config {
	return Config{Mode: ModeAuto}
}
