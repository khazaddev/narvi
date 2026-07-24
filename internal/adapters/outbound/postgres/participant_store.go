package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// ParticipantStore is a thin, pass-through wrapper around the
// sqlc-generated participants queries (migrations/000011_participants.
// up.sql). Nothing populates this table yet (§8.11's own "distinct,
// not-yet-scoped concern") -- Step 37's own authorization stopgap
// predicate (internal/adapters/inbound/httpapi/planauthz.go's
// canActOnPlan) is this store's first real reader, querying it
// defensively even though it will only ever find rows once a later Step
// starts writing them.
type ParticipantStore struct {
	q *sqlcgen.Queries
}

// NewParticipantStore builds a ParticipantStore backed by pool.
func NewParticipantStore(pool *pgxpool.Pool) *ParticipantStore {
	return &ParticipantStore{q: sqlcgen.New(pool)}
}

// WithTx returns a ParticipantStore whose queries run on tx instead of the
// pool this store was built with -- used when the caller's own
// authorization check must run inside an already-open transaction (e.g.
// planapprove.go, alongside its own session-row lock).
func (s *ParticipantStore) WithTx(tx pgx.Tx) *ParticipantStore {
	return &ParticipantStore{q: s.q.WithTx(tx)}
}

// Exists reports whether a participants row already exists for
// (sessionID, userID).
func (s *ParticipantStore) Exists(ctx context.Context, sessionID, userID pgtype.UUID) (bool, error) {
	return s.q.ParticipantExists(ctx, sqlcgen.ParticipantExistsParams{SessionID: sessionID, UserID: userID})
}
