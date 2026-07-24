package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// AuditLogStore is a thin, pass-through wrapper around the sqlc-generated
// audit_log query (§13.3, migrations/000013_audit_log.up.sql). No
// caching, no retries, no business rules -- Step 39 ("identities + full
// RBAC") is the first caller of Record, and every call site uses
// WithTx(tx), never the pool-scoped form directly, since §13.3 requires
// the audit row be "written in the same transaction as the change".
type AuditLogStore struct {
	q *sqlcgen.Queries
}

// NewAuditLogStore builds an AuditLogStore backed by pool.
func NewAuditLogStore(pool *pgxpool.Pool) *AuditLogStore {
	return &AuditLogStore{q: sqlcgen.New(pool)}
}

// WithTx returns an AuditLogStore whose queries run on tx instead of the
// pool this store was built with -- mirrors every other store's own
// identical WithTx convention (e.g. OutboxStore, ParticipantStore). This
// is the ONLY way callers are expected to use AuditLogStore: an audit row
// belongs in the SAME transaction as the state change it records, never a
// separate, later write that could commit independently of it.
func (s *AuditLogStore) WithTx(tx pgx.Tx) *AuditLogStore {
	return &AuditLogStore{q: s.q.WithTx(tx)}
}

// Record inserts a new audit_log row and returns it. ActorUserID is
// nullable (pgtype.UUID{} for a bot/webhook-attributed change -- see
// queries/audit_log.sql's own doc comment); DetailJson defaults to '{}'
// at the schema level if a caller passes nil/empty, but every call site in
// this codebase supplies a real, small JSON object describing what
// changed.
func (s *AuditLogStore) Record(ctx context.Context, arg sqlcgen.CreateAuditLogEntryParams) (sqlcgen.AuditLog, error) {
	return s.q.CreateAuditLogEntry(ctx, arg)
}

// List returns up to limit audit_log rows, newest first, skipping offset
// rows -- backs the members API's own read endpoint over the audit log
// (§13.3: "surfaced in Settings -> Members ('Audit log')", Step 39's own
// second half). Always the pool-scoped form (never WithTx) -- a read has
// no transactional-consistency requirement with any in-flight write the
// way Record's own callers do.
func (s *AuditLogStore) List(ctx context.Context, limit, offset int32) ([]sqlcgen.AuditLog, error) {
	return s.q.ListAuditLogEntries(ctx, sqlcgen.ListAuditLogEntriesParams{Limit: limit, Offset: offset})
}
