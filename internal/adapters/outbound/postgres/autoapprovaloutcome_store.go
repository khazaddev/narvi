package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// AutoApprovalOutcomeStore is a thin, pass-through wrapper around the
// sqlc-generated auto_approval_outcomes queries (§21.2 stage 2)
// -- see migrations/000070_auto_approval_outcomes.up.sql's own doc
// comment for the contradiction-rate calibration read model's full
// design.
type AutoApprovalOutcomeStore struct {
	q *sqlcgen.Queries
}

// NewAutoApprovalOutcomeStore builds an AutoApprovalOutcomeStore backed
// by pool.
func NewAutoApprovalOutcomeStore(pool *pgxpool.Pool) *AutoApprovalOutcomeStore {
	return &AutoApprovalOutcomeStore{q: sqlcgen.New(pool)}
}

// Record idempotently records outcome for (repoFullName, prNumber,
// headSHA) -- see RecordAutoApprovalOutcome's own generated doc comment
// for the "first recorded outcome wins, never overwritten" precedent.
// suppressedInShadow marks an outcome observed while this repository's
// egress was suppressed. It is still RECORDED -- the operator ledger
// shows it -- and excluded from the contradiction rate, per §30.7.
func (s *AutoApprovalOutcomeStore) Record(ctx context.Context, repoFullName string, prNumber int32, headSHA, outcome string, suppressedInShadow bool) error {
	return s.q.RecordAutoApprovalOutcome(ctx, sqlcgen.RecordAutoApprovalOutcomeParams{
		RepoFullName:       repoFullName,
		PrNumber:           prNumber,
		HeadSha:            headSHA,
		Outcome:            outcome,
		SuppressedInShadow: suppressedInShadow,
	})
}

// CountInWindow returns repoFullName's own total and contested (outcome
// = 'overridden') counts since sinceTime -- internal/domain/reviewverdict.
// ContradictionRate's own caller reduces these two plain integers.
func (s *AutoApprovalOutcomeStore) CountInWindow(ctx context.Context, repoFullName string, sinceTime pgtype.Timestamptz) (total, contested int64, err error) {
	row, err := s.q.CountAutoApprovalOutcomesInWindow(ctx, sqlcgen.CountAutoApprovalOutcomesInWindowParams{
		RepoFullName: repoFullName,
		DecidedAt:    sinceTime,
	})
	if err != nil {
		return 0, 0, err
	}
	return row.Total, row.Contested, nil
}
