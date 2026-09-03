package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// SlackThreadSessionStore is a thin, pass-through wrapper around the
// sqlc-generated slack_thread_sessions queries (§8.10's thread↔session
// mapping "Slack ingress"). No caching, no retries, no business
// rules -- see migrations/000029_slack_thread_sessions.up.sql's own doc
// comment for the atomic-claim design this wraps.
type SlackThreadSessionStore struct {
	q *sqlcgen.Queries
}

// NewSlackThreadSessionStore builds a SlackThreadSessionStore backed by
// pool.
func NewSlackThreadSessionStore(pool *pgxpool.Pool) *SlackThreadSessionStore {
	return &SlackThreadSessionStore{q: sqlcgen.New(pool)}
}

// WithTx returns a SlackThreadSessionStore whose queries run on tx instead
// of the pool this store was built with -- mirrors WebhookDeliveryStore's
// own WithTx convention exactly.
func (s *SlackThreadSessionStore) WithTx(tx pgx.Tx) *SlackThreadSessionStore {
	return &SlackThreadSessionStore{q: s.q.WithTx(tx)}
}

// Claim attempts an atomic first-writer-wins claim of (channelID,
// threadTS) for sessionID -- see ClaimSlackThreadSession's own doc
// comment (postgres/queries/slackthreadsessions.sql) for the full
// ON CONFLICT DO NOTHING reasoning. ok reports whether THIS call actually
// won the claim (true -- sessionID is now the thread's own mapped
// session) or lost it to a concurrent racer (false, err == nil -- the
// caller must look up the winner's real session via Get instead). Any
// other error is a genuine failure, distinct from "lost the race".
func (s *SlackThreadSessionStore) Claim(ctx context.Context, channelID, threadTS string, sessionID pgtype.UUID) (row sqlcgen.SlackThreadSession, ok bool, err error) {
	row, err = s.q.ClaimSlackThreadSession(ctx, sqlcgen.ClaimSlackThreadSessionParams{
		ChannelID: channelID,
		ThreadTs:  threadTS,
		SessionID: sessionID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlcgen.SlackThreadSession{}, false, nil
		}
		return sqlcgen.SlackThreadSession{}, false, err
	}
	return row, true, nil
}

// Get fetches an existing thread's mapped session, returning pgx.ErrNoRows
// (unwrapped, exactly as pgx returns it) when no mapping exists yet for
// (channelID, threadTS) -- callers use errors.Is(err, pgx.ErrNoRows) the
// same way every other Get in this package's own Store types already
// does (e.g. SessionStore.Get).
func (s *SlackThreadSessionStore) Get(ctx context.Context, channelID, threadTS string) (sqlcgen.SlackThreadSession, error) {
	return s.q.GetSlackThreadSession(ctx, sqlcgen.GetSlackThreadSessionParams{
		ChannelID: channelID,
		ThreadTs:  threadTS,
	})
}

// GetBySessionID is the REVERSE lookup §5.1 ("outbox delivery") needs:
// given a session_id, which (channel_id, thread_ts) thread does it back?
// Returns pgx.ErrNoRows (unwrapped) when sessionID was never created via a
// Slack thread.
func (s *SlackThreadSessionStore) GetBySessionID(ctx context.Context, sessionID pgtype.UUID) (sqlcgen.SlackThreadSession, error) {
	return s.q.GetSlackThreadSessionBySessionID(ctx, sessionID)
}
