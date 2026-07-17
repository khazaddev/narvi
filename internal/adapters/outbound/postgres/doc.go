// Package postgres holds the Postgres-backed stores (§5.1). PR-04 landed
// the core schema migrations (../../../../migrations), sqlc codegen
// (sqlcgen, generated from ./queries/*.sql), NewPool, and thin store
// skeletons for exactly the 5 ports §4.3 names as sqlc-backed:
// SessionStore, TurnStore, SandboxStore, OutboxStore, TimerStore. Each is
// a pass-through wrapper around *sqlcgen.Queries — no caching, no
// retries, no business rules.
//
// Stores for the other 7 core tables (users, identities, sandbox_history,
// events, participants, artifacts, audit_log) are still pending: they get
// built by the PRs that actually consume them (auth/identity for
// users/identities, PR-18/19 for events, §8.11 multiplayer for
// participants, PR-21+ for artifacts, PR-39 for audit_log).
package postgres
