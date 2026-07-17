# migrations

Postgres migrations (golang-migrate) for the core schema, landed in **PR-04**
(§1, §2, §5.1, §13.2): `users`, `identities`, `sessions`, `turns`,
`sandboxes` (+ `sandbox_history`), `events`, `session_timers`, `outbox`,
`participants`, `artifacts`, and `audit_log`.

Sequential numbered files (`NNNNNN_description.up.sql` /
`NNNNNN_description.down.sql`), one logical unit (table, or the pgcrypto
extension) per file. Each `.down.sql` undoes only what its own `.up.sql`
created; golang-migrate's reverse-order execution handles FK ordering
across files.

`embed.go` embeds `*.sql` into `migrations.FS` (`//go:embed`) so the
`iofs` source driver can run these migrations from the compiled binary
without external file access (§12.1, single-binary self-host story). See
`internal/adapters/outbound/postgres/postgres_integration_test.go` for the
proof this embed + golang-migrate + sqlc pipeline works end to end against
a real Postgres instance.
