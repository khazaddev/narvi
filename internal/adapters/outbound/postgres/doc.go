// Package postgres holds the Postgres-backed stores (§5.1). PR-04 landed
// the core schema migrations (../../../../migrations), sqlc codegen
// (sqlcgen, generated from ./queries/*.sql), NewPool, and thin store
// skeletons for exactly the 5 ports §4.3 names as sqlc-backed:
// SessionStore, TurnStore, SandboxStore, OutboxStore, TimerStore. Each is
// a pass-through wrapper around *sqlcgen.Queries — no caching, no
// retries, no business rules.
//
// UserStore, IdentityStore, EventStore, and ArtifactStore have since
// landed too, exactly as predicted below (auth/identity's §13.1 for
// users/identities, §3.2/§6.2 for events, §6.2 for artifacts) —
// alongside UserSessionStore (§13.1's user_sessions table) and
// WSTokenStore (§6.2's ws_tokens table), on top of this package's
// original 12-table scope.
//
// Stores for the remaining 3 core tables (sandbox_history, participants,
// audit_log) are still pending: they get built by the PRs that actually
// consume them (§8.11 multiplayer for participants, PR-39 for audit_log,
// sandbox_history whenever its own consumer lands).
package postgres
