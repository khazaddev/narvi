# migrations

Postgres migrations (goose or golang-migrate) for the core schema: `users`,
`identities`, `sessions`, `turns`, `sandboxes` (+ `sandbox_history`),
`events`, `session_timers`, `outbox`, `participants`, `artifacts`, and
`audit_log` (§2, §5.1, §13.2).

This directory is populated in **PR-04** (§1, §2, §5.1, §13.2): the core
schema migrations plus sqlc-generated store skeletons.
