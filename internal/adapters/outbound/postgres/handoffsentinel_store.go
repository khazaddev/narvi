package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// HandoffSentinelStore is a thin, pass-through wrapper around the
// sqlc-generated handoff_sentinel_runs queries ("handoff-readiness
// sentinel", §14.4) -- see migrations/000049_handoff_sentinel_runs.up.sql's
// own doc comment for the table's full design and its single-step claim
// idiom.
type HandoffSentinelStore struct {
	q *sqlcgen.Queries
}

// NewHandoffSentinelStore builds a HandoffSentinelStore backed by pool.
func NewHandoffSentinelStore(pool *pgxpool.Pool) *HandoffSentinelStore {
	return &HandoffSentinelStore{q: sqlcgen.New(pool)}
}

// WithTx returns a HandoffSentinelStore whose queries run on tx instead of
// the pool this store was built with -- mirrors every other store's own
// identical WithTx convention. Claim below MUST be called on a
// WithTx-scoped store, inside the SAME transaction that enqueues the
// outbox row for this same PR (internal/app/sessionactor/handoffsentinel.go),
// exactly mirroring reviewverdict.go's own "claim + outbox-enqueue in one
// transaction" precedent for sentinel_fixes.
func (s *HandoffSentinelStore) WithTx(tx pgx.Tx) *HandoffSentinelStore {
	return &HandoffSentinelStore{q: s.q.WithTx(tx)}
}

// Claim attempts an atomic first-writer-wins claim on (repoFullName,
// prNumber) -- (true, nil) means THIS call won the claim (no row existed
// before; the caller should proceed to post the comment/sync the label
// and enqueue exactly one outbox row); (false, nil) means a row already
// existed (an earlier run, or a concurrent/duplicate invocation for the
// SAME PR already claimed it -- the caller must post NOTHING, satisfying
// "running the sentinel twice must not duplicate the label, the comment,
// or the issue"). Any other error is a genuine, unexpected failure.
func (s *HandoffSentinelStore) Claim(ctx context.Context, repoFullName string, prNumber int32, sessionID pgtype.UUID) (bool, error) {
	_, err := s.q.ClaimHandoffSentinelRun(ctx, sqlcgen.ClaimHandoffSentinelRunParams{
		RepoFullName: repoFullName,
		PrNumber:     prNumber,
		SessionID:    sessionID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// ON CONFLICT DO NOTHING found an existing row -- already
			// claimed, not a failure.
			return false, nil
		}
		return false, err
	}
	return true, nil
}
