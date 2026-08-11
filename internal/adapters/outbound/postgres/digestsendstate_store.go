package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// DigestSendStateStore is a thin, pass-through wrapper around the
// sqlc-generated digest_send_state queries (Step 62, §21.3) -- see
// migrations/000071_digest_send_state.up.sql's own doc comment for the
// full two-phase (idempotent seed, then SKIP LOCKED claim) at-most-one-
// send-per-channel-per-day design.
type DigestSendStateStore struct {
	q *sqlcgen.Queries
}

// NewDigestSendStateStore builds a DigestSendStateStore backed by pool.
func NewDigestSendStateStore(pool *pgxpool.Pool) *DigestSendStateStore {
	return &DigestSendStateStore{q: sqlcgen.New(pool)}
}

// Seed idempotently ensures a 'pending' row exists for (sendDate,
// channelProvider, channelID) -- ok=false (not an error) means a row
// already existed (this tick's own INSERT ... ON CONFLICT DO NOTHING was
// a no-op, mirrored here as pgx.ErrNoRows from sqlc's own :one query
// shape), the ordinary, expected case on every tick after the first that
// discovers the same channel.
func (s *DigestSendStateStore) Seed(ctx context.Context, sendDate pgtype.Date, channelProvider, channelID string) (row sqlcgen.DigestSendState, ok bool, err error) {
	row, err = s.q.SeedDigestSendState(ctx, sqlcgen.SeedDigestSendStateParams{
		SendDate:        sendDate,
		ChannelProvider: channelProvider,
		ChannelID:       channelID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlcgen.DigestSendState{}, false, nil
		}
		return sqlcgen.DigestSendState{}, false, err
	}
	return row, true, nil
}

// ClaimPending atomically claims up to limit still-'pending' rows for
// sendDate -- see ClaimPendingDigestSendState's own generated doc comment
// for the SELECT ... FOR UPDATE SKIP LOCKED + UPDATE-to-'sending' design.
func (s *DigestSendStateStore) ClaimPending(ctx context.Context, sendDate pgtype.Date, limit int32) ([]sqlcgen.DigestSendState, error) {
	return s.q.ClaimPendingDigestSendState(ctx, sqlcgen.ClaimPendingDigestSendStateParams{
		SendDate: sendDate,
		Limit:    limit,
	})
}

// MarkSent records id as successfully handed off to the outbox (see
// MarkDigestSendStateSent's own generated doc comment for what "sent"
// means here specifically).
func (s *DigestSendStateStore) MarkSent(ctx context.Context, id pgtype.UUID) error {
	return s.q.MarkDigestSendStateSent(ctx, id)
}

// MarkFailed records id as a terminal failure for this send_date, with
// lastError for operator visibility -- never retried the same day (see
// MarkDigestSendStateFailed's own generated doc comment).
func (s *DigestSendStateStore) MarkFailed(ctx context.Context, id pgtype.UUID, lastError string) error {
	return s.q.MarkDigestSendStateFailed(ctx, sqlcgen.MarkDigestSendStateFailedParams{
		ID:        id,
		LastError: &lastError,
	})
}
