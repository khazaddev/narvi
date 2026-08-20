// Package sandboxsecret implements Step 72's own ("sandbox secrets &
// opencode config", §27.1) pure domain vocabulary for the general
// sandbox_secrets table (migrations/000090_sandbox_secrets.up.sql) --
// this codebase's SECOND generic secret-storage table, after Step 53's
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
// into). This is not merely a documented limitation; as of this Step
// there is no privilege boundary between the coding agent and
// sandbox-agent at all (internal/sandboxagent/supervisor.Spawn sets only
// Setpgid in its own SysProcAttr, no UID drop) -- so ANYTHING in
// sandbox-agent's own process environment or on its filesystem (the
// bearer token, the credential cache, and now these secrets) is already
// readable by the agent's own tools via /proc/<sandbox-agent-pid>/environ,
// same-UID, regardless of which specific process a value was injected
// into. OS-level isolation (a UID drop / user namespace) is §30.5's own
// scope, not this one's -- see that Step's own row in
// docs/IMPLEMENTATION_PLAN.md for the named debt. The real boundaries
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
