package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// OutboxStore is a thin, pass-through wrapper around the sqlc-generated
// outbox queries (§4.3 Outbox, §5.1 outbox pattern). No caching, no
// retries, no business rules -- the claim/attempt/record delivery loop
// lives in internal/app/outboxworker (Step 35, "outbox delivery").
type OutboxStore struct {
	q *sqlcgen.Queries
}

// NewOutboxStore builds an OutboxStore backed by pool.
func NewOutboxStore(pool *pgxpool.Pool) *OutboxStore {
	return &OutboxStore{q: sqlcgen.New(pool)}
}

// WithTx returns an OutboxStore whose queries run on tx instead of the
// pool this store was built with — mirrors the same WithTx convention
// every other store in this package already follows (e.g. EventStore),
// ready for app/sessionactor's transactional-write helper (§2) once a
// caller starts writing outbox entries inside that transaction; no such
// caller exists yet.
func (s *OutboxStore) WithTx(tx pgx.Tx) *OutboxStore {
	return &OutboxStore{q: s.q.WithTx(tx)}
}

// Create inserts a new outbox entry and returns it.
func (s *OutboxStore) Create(ctx context.Context, arg sqlcgen.CreateOutboxEntryParams) (sqlcgen.Outbox, error) {
	return s.q.CreateOutboxEntry(ctx, arg)
}

// Get fetches an outbox entry by id.
func (s *OutboxStore) Get(ctx context.Context, id pgtype.UUID) (sqlcgen.Outbox, error) {
	return s.q.GetOutboxEntry(ctx, id)
}

// ListDuePending returns up to limit 'pending' rows eligible to (re)attempt
// delivery now, locked FOR UPDATE SKIP LOCKED -- callers MUST run this
// inside the same transaction that subsequently calls Claim on each
// returned row (see ListDuePendingOutboxEntries's own generated doc
// comment).
func (s *OutboxStore) ListDuePending(ctx context.Context, limit int32) ([]sqlcgen.Outbox, error) {
	return s.q.ListDuePendingOutboxEntries(ctx, limit)
}

// Claim bumps id's row next_attempt_at forward by claimProtection (a
// provisional protection window, see ClaimOutboxEntry's own generated doc
// comment for why the outbox table needs this rather than a distinct
// in-flight status) and increments attempts -- the commit-before-the-
// real-notifier-call half of outboxworker.Builder's own two-step shape.
func (s *OutboxStore) Claim(ctx context.Context, id pgtype.UUID, nextAttemptAt pgtype.Timestamptz) (sqlcgen.Outbox, error) {
	return s.q.ClaimOutboxEntry(ctx, sqlcgen.ClaimOutboxEntryParams{
		ID:            id,
		NextAttemptAt: nextAttemptAt,
	})
}

// MarkDelivered records a successful delivery. Returns pgx.ErrNoRows if
// id's row is no longer 'pending' (an already-superseded/stale outcome --
// see MarkOutboxEntryDelivered's own generated doc comment).
func (s *OutboxStore) MarkDelivered(ctx context.Context, id pgtype.UUID) (sqlcgen.Outbox, error) {
	return s.q.MarkOutboxEntryDelivered(ctx, id)
}

// RecordFailure records a failed delivery attempt still eligible for
// another retry: nextAttemptAt is the caller's own domain/outbox.
// EvaluateBackoff-computed value, lastError captures the notifier's own
// error for observability. Returns pgx.ErrNoRows if id's row is no longer
// 'pending', mirroring MarkDelivered's own identical guard.
func (s *OutboxStore) RecordFailure(ctx context.Context, id pgtype.UUID, nextAttemptAt pgtype.Timestamptz, lastError string) (sqlcgen.Outbox, error) {
	return s.q.RecordOutboxEntryFailure(ctx, sqlcgen.RecordOutboxEntryFailureParams{
		ID:            id,
		NextAttemptAt: nextAttemptAt,
		LastError:     &lastError,
	})
}

// MarkDeadLetter records a failed delivery attempt that has exhausted
// domain/outbox.MaxAttempts. Returns pgx.ErrNoRows if id's row is no
// longer 'pending', mirroring MarkDelivered/RecordFailure's own identical
// guard.
func (s *OutboxStore) MarkDeadLetter(ctx context.Context, id pgtype.UUID, lastError string) (sqlcgen.Outbox, error) {
	return s.q.MarkOutboxEntryDeadLetter(ctx, sqlcgen.MarkOutboxEntryDeadLetterParams{
		ID:        id,
		LastError: &lastError,
	})
}
