// Package environment implements Environment scoping (§14.1, "Scoped
// Environments"): "product/PM sessions are almost exclusively frontend
// work, but an unscoped agent will happily wander into backend files --
// that work is then thrown away and redone by an engineer." §14.1's fix is
// prevention, not correction, and is explicit that "enforcement is at the
// git layer, not a policy/prompt layer": excluded paths never materialize
// on the sandbox filesystem at all, so there is nothing for an agent (or a
// prompt injection, or a gap in OpenCode's own permission model) to reach
// past.
//
// Unlike internal/domain/{sandbox,turn,gitstate}, this package is NOT an
// independent Transition(from, trigger) state machine -- there is no
// lifecycle here with triggers to model. An Environment's path_scope is
// set once (by whatever later Step owns Environment configuration) and
// then simply IS scoped or unscoped; nothing transitions it. This makes
// the package's shape closer to internal/domain/session's status
// derivation: a validated value type (Environment) plus a small number of
// pure derivation functions over it, not a table of legal edges.
//
//   - Environment (environment.go) carries path_scope as PathScope --
//     nil/empty meaning full access, unchanged behavior, exactly as
//     §14.1 specifies -- and MockConfigured/ContractsPath, together
//     recording whether a mock_config is attached and, if so, the
//     repo-relative path to the contract-driven mock spec directory its
//     sessions check for drift against (§14.3, landed in Step 27: "a
//     shared contracts/api/*.{yaml,json} spec... drives a generated mock
//     server"). ContractsPath is empty exactly when MockConfigured is
//     false -- the two are set together, at session-creation time, by
//     httpapi.CreateSession.
//   - ValidatePathScope (environment.go) validates a candidate path_scope
//     before it is accepted onto an Environment, rejecting each of three
//     independent problems as its own named sentinel error
//     (ErrEmptyPattern, ErrPathTraversal, ErrInvalidGlobSyntax), wrapped
//     by a single typed *InvalidGlobError carrying the offending pattern
//     and the sentinel it unwraps to -- matching the sentinel-error house
//     style already used in internal/domain/{sandbox,turn,gitstate}
//     (IllegalTransitionError/StaleGenError wrapping ErrIllegalTransition/
//     ErrStaleGen), just consolidated into one struct shape here because
//     all three problems share the same one field worth reporting (the
//     pattern itself) rather than needing per-reason struct shapes.
//   - IsScoped, SparseCheckoutPatterns, and RequiresProvenanceTag
//     (environment.go) are the pure accessors §14.1 needs from two later,
//     different callers: SparseCheckoutPatterns feeds the clone step
//     ("domain/gitstate's clone step (§3.4) runs `git sparse-checkout set
//     <globs>` per repo when path_scope is present") and
//     RequiresProvenanceTag feeds session creation ("Sessions created
//     under a scoped Environment carry a provenance tag ... so the label
//     automation and the handoff sentinel (§14.4) can act on it without
//     re-deriving intent"). Both are one-line delegations to IsScoped
//     today, but are named separately because they document two distinct
//     caller-facing concepts -- a git-layer decision and a session-level
//     decision -- that happen to coincide now and may not always.
//
// This package performs zero I/O of any kind (§11: "Never put I/O,
// time.Now(), or randomness in /internal/domain"). It does not shell out
// to git, does not run sparse-checkout, and does not decide the on-disk
// sparse-checkout mechanics (cone mode, .git/info/sparse-checkout
// contents, etc.) -- it only decides WHAT glob patterns a scoped
// Environment implies and whether a given pattern is well-formed and safe.
// Actually invoking `git sparse-checkout` inside a sandbox is Step 29's
// job ("gitstate in-sandbox", sandbox-agent), not this one's.
package environment
