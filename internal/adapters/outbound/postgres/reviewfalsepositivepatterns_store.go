package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// FalsePositivePatternStore is a thin, pass-through wrapper around the
// sqlc-generated review_false_positive_patterns queries (Step 63, "review:
// learned false-positive patterns", §22.2/§22.4) -- see migrations/
// 000073_review_false_positive_patterns.up.sql's own doc comment for the
// table's full design. No caching, no retries, no business rules --
// callers (internal/adapters/inbound/github's capture handler, internal/
// adapters/inbound/httpapi's audit-view/retire handlers, internal/app/
// reviewcontext's advisory-injection fetch) decide what a missing row
// means for their own purposes.
type FalsePositivePatternStore struct {
	q *sqlcgen.Queries
}

// NewFalsePositivePatternStore builds a FalsePositivePatternStore backed by
// pool.
func NewFalsePositivePatternStore(pool *pgxpool.Pool) *FalsePositivePatternStore {
	return &FalsePositivePatternStore{q: sqlcgen.New(pool)}
}

// WithTx returns a FalsePositivePatternStore whose queries run on tx
// instead of the pool this store was built with -- mirrors every other
// store's own identical WithTx convention (e.g. ReviewFindingStore.WithTx).
func (s *FalsePositivePatternStore) WithTx(tx pgx.Tx) *FalsePositivePatternStore {
	return &FalsePositivePatternStore{q: s.q.WithTx(tx)}
}

// Upsert creates-or-updates one review_false_positive_patterns row keyed on
// commentID -- see UpsertFalsePositivePattern's own generated doc comment
// for why every column but the INSERT's own initial values is preserved on
// a redelivered/retried capture of the SAME comment id. inserted reports
// whether this call captured a genuinely new pattern (true) or observed an
// already-known comment id (false) -- callers use it purely for
// logging/observability, mirroring ClaimWebhookDelivery's identical
// Inserted-is-log-only convention.
func (s *FalsePositivePatternStore) Upsert(ctx context.Context, repoFullName string, commentID int64, reason string, createdBy pgtype.UUID) (row sqlcgen.ReviewFalsePositivePattern, inserted bool, err error) {
	r, err := s.q.UpsertFalsePositivePattern(ctx, sqlcgen.UpsertFalsePositivePatternParams{
		RepoFullName: repoFullName,
		CommentID:    commentID,
		Reason:       reason,
		CreatedBy:    createdBy,
	})
	if err != nil {
		return sqlcgen.ReviewFalsePositivePattern{}, false, err
	}
	return sqlcgen.ReviewFalsePositivePattern{
		ID:           r.ID,
		RepoFullName: r.RepoFullName,
		CommentID:    r.CommentID,
		Reason:       r.Reason,
		CreatedBy:    r.CreatedBy,
		CreatedAt:    r.CreatedAt,
		HitCount:     r.HitCount,
		LastHitAt:    r.LastHitAt,
		RetiredAt:    r.RetiredAt,
		RetiredBy:    r.RetiredBy,
	}, r.Inserted, nil
}

// Get fetches one pattern by id -- pgx.ErrNoRows (unwrapped) means no such
// pattern was ever taught.
func (s *FalsePositivePatternStore) Get(ctx context.Context, id pgtype.UUID) (sqlcgen.ReviewFalsePositivePattern, error) {
	return s.q.GetFalsePositivePattern(ctx, id)
}

// ListActive returns every currently-active (not retired) pattern for
// repoFullName, oldest-first -- internal/app/reviewcontext's own §22.3
// advisory-injection read.
func (s *FalsePositivePatternStore) ListActive(ctx context.Context, repoFullName string) ([]sqlcgen.ReviewFalsePositivePattern, error) {
	return s.q.ListActiveFalsePositivePatterns(ctx, repoFullName)
}

// List returns every pattern (active or retired) for repoFullName,
// newest-first, bounded by limit -- §22.4's own audit view.
func (s *FalsePositivePatternStore) List(ctx context.Context, repoFullName string, limit int32) ([]sqlcgen.ReviewFalsePositivePattern, error) {
	return s.q.ListFalsePositivePatterns(ctx, sqlcgen.ListFalsePositivePatternsParams{
		RepoFullName: repoFullName,
		Limit:        limit,
	})
}

// Retire records a maintainer+'s explicit retirement of pattern id (§22.4)
// -- pgx.ErrNoRows (unwrapped) means EITHER no pattern with this id exists
// at all, OR it exists but is already retired (the guarded UPDATE's own
// WHERE retired_at IS NULL clause -- see RetireFalsePositivePattern's own
// generated doc comment); callers distinguish the two with a follow-up Get
// on this same error path.
func (s *FalsePositivePatternStore) Retire(ctx context.Context, id, retiredBy pgtype.UUID) (sqlcgen.ReviewFalsePositivePattern, error) {
	return s.q.RetireFalsePositivePattern(ctx, sqlcgen.RetireFalsePositivePatternParams{
		ID:        id,
		RetiredBy: retiredBy,
	})
}

// IncrementHitCount bumps hit_count/last_hit_at for every id in ids --
// §22.4's own usage-signal bookkeeping, called once per review pass with
// every pattern id that pass's advisory block actually included. A no-op
// (never a store-level error) when ids is empty -- mirrors this codebase's
// own "an empty batch is never worth a round trip" convention.
func (s *FalsePositivePatternStore) IncrementHitCount(ctx context.Context, ids []pgtype.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	return s.q.IncrementFalsePositivePatternHitCount(ctx, ids)
}
