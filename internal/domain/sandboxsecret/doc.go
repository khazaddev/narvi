// Package sandboxsecret implements §27.1's own ("sandbox secrets &
// opencode config", §27.1) pure domain vocabulary for the general
// sandbox_secrets table (migrations/000090_sandbox_secrets.up.sql) --
// this codebase's SECOND generic secret-storage table, after §25.1's
// provider_credentials (internal/domain/providercredential).
//
// # Why a second table, not a widened provider_credentials
//
// provider_credentials' own identity column is a closed Postgres ENUM of
// 3 provider names, each mapped to fixed OpenCode env-var name(s) by
// domain code (providercredential.EnvVarNames), consumed by exactly one
// process (opencode serve). sandbox_secrets inverts both properties: its
// identity IS a user-chosen env-var name (this package's own ValidateName,
// name.go), and its consumers are the WHOLE supervised process tree
// (hooks, services.yml services, the agent's own shell) -- widening
// provider_credentials' closed-vocabulary provider column into free text
// would destroy the property its own docs already treat as load-bearing.
// See migrations/000090_sandbox_secrets.up.sql's own top comment for the
// same reasoning at the schema layer.
//
// # Scope/resolution: reused, not reinvented
//
// sandbox_secrets resolves through internal/domain/providercredential's
// OWN Scope type and Resolve function -- §27.1's own explicit instruction
// ("resolution automation -> environment -> repo -> global via the SAME
// generic providercredential.Resolve... only the scopePriority table
// gains the fourth row"). This package therefore holds NO Scope type, NO
// Candidate type, and NO Resolve function of its own -- see
// providercredential's own doc.go for the full "why this package, not a
// new one" reasoning and the ScopeAutomation addition. What THIS package
// owns instead is the one piece of domain logic sandbox_secrets needs
// that provider_credentials never did: validating a user-CHOSEN name.
//
// # Name validation is the whole point of this package
//
// A provider_credentials row's identity (Provider) is drawn from a closed,
// code-controlled set -- there is nothing for a caller to validate beyond
// "is this one of the 3 known values", already covered by
// providercredential.IsValidProvider. A sandbox_secrets row's identity
// (Name) is instead whatever string an admin/maintainer types into
// Settings, and that string becomes a REAL environment variable name
// inside every hook, service, and opencode serve process a session spawns
// -- so it must be fail-closed validated at SAVE time (ValidateName,
// name.go), per §27.1's own explicit rule: POSIX env-var shape, the
// NARVI_* namespace rejected outright (§19.8's own reservation), and every
// name providercredential.EnvVarNames already owns (ANTHROPIC_API_KEY,
// OPENAI_API_KEY, the 3 Google names) rejected too -- "one owning
// mechanism per env-var name" (§27.1), so a collision between the two
// secret-storage tables over the SAME env-var name is unrepresentable, not
// merely discouraged. This is enforced entirely at the CRUD write path
// (internal/adapters/inbound/httpapi's sandbox_secrets management
// handlers) -- nothing at delivery/injection time re-checks it, exactly
// mirroring providercredentials.go's own NUL-byte check precedent (checked
// once, at write time, never re-verified on the read/delivery path).
//
// # No I/O, no time.Now(), no randomness (CLAUDE.md §11)
//
// ValidateName is a pure function over its own string argument -- no
// database lookup, no clock, no randomness. It cannot itself confirm a
// name is not ALREADY taken at a different (scope, scopeTargetID) pair
// for the SAME provider_credentials-reserved name (that is a Postgres
// UNIQUE-index concern, migrations/000090_sandbox_secrets.up.sql's own
// partial-unique-index pair, mirroring migration 000056's identical
// shape) -- this package validates the NAME ITSELF, in isolation, the one
// check that has nothing to do with what else is already stored.
//
// # What this mechanism does NOT claim (§27.1, stated here too)
//
// In-sandbox secrecy from the CODING AGENT is a non-goal -- the agent is
// the intended consumer of every sandbox_secrets value (it runs the
// hooks/services/opencode serve process tree these values are injected
// into). This paragraph used to say there was no privilege boundary at
// all between the coding agent and sandbox-agent, and that anything in
// sandbox-agent's environment or filesystem was readable same-UID through
// /proc. That was true when it was written and is no longer: §30.5's UID
// drop landed, so the runtime spawns under its own uid and the bearer
// token, the credential cache and sandbox-agent's own environ are
// unreadable to it by kernel enforcement.
//
// What that changes for THIS type is narrower than it sounds, and the
// narrowing is the point: a sandbox_secrets value injected into the
// runtime's own environment is still fully visible to the runtime, which
// is the intended consumer. Isolation protects what the runtime was never
// handed; it cannot protect what it was handed by design. So the posture
// below is unchanged -- these values are readable by the agent, and the
// boundaries that matter are the ones listed next. The real
// boundaries
// sandbox_secrets DOES hold today: encrypted at rest CP-side
// (platform.EncryptToken, mirroring provider_credentials'
// value_encrypted exactly), never delivered through the sandbox
// PROVIDER's own API (only over the authenticated sandbox<->CP delivery
// channel, §27.1's own POST /sessions/{id}/sandbox-secrets), never
// written inside any repo working tree (so never committable), never
// logged, and structurally never reaching ports.SandboxProvider.BuildImage
// (§19.8 rule (a) -- see cmd/sandbox-agent/main.go's own doc comment on
// why that is true by construction, not merely by convention).
package sandboxsecret
