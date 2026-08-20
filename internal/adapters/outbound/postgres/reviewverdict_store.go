package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// ReviewVerdictStore is a thin, pass-through wrapper around the
// sqlc-generated review_verdicts queries (§21.1) -- see
// migrations/000067_review_verdicts.up.sql's own doc comment for the
// table's full append-only design. No caching, no retries, no business
// rules -- callers (internal/app/reviewverdict) decide what a missing
// row (pgx.ErrNoRows, unwrapped) means for their own purposes, mirroring
// every other Store in this package.
type ReviewVerdictStore struct {
	q *sqlcgen.Queries
}

// NewReviewVerdictStore builds a ReviewVerdictStore backed by pool.
func NewReviewVerdictStore(pool *pgxpool.Pool) *ReviewVerdictStore {
	return &ReviewVerdictStore{q: sqlcgen.New(pool)}
}

// WithTx returns a ReviewVerdictStore whose queries run on tx --
// httpapi.PostReviewVerdict's own caller uses this so the new
// review_verdicts row commits atomically alongside that handler's
// existing review_findings upserts and outbox write (all-or-nothing,
// mirroring that handler's own established transaction boundary).
func (s *ReviewVerdictStore) WithTx(tx pgx.Tx) *ReviewVerdictStore {
	return &ReviewVerdictStore{q: s.q.WithTx(tx)}
}

// Insert appends one review_verdicts row -- see InsertReviewVerdict's own
// generated doc comment. The ONE write this table ever accepts.
func (s *ReviewVerdictStore) Insert(ctx context.Context, arg sqlcgen.InsertReviewVerdictParams) (sqlcgen.ReviewVerdict, error) {
	return s.q.InsertReviewVerdict(ctx, arg)
}

// GetLatest fetches (repoFullName, prNumber)'s own most-recently-posted
// verdict. pgx.ErrNoRows (unwrapped) means no verdict has ever been
// posted for this PR -- callers (the auto-approval eligibility engine,
// the decision inbox's own classification) treat that identically to an
// ineligible/needs-review PR, never as an error.
func (s *ReviewVerdictStore) GetLatest(ctx context.Context, repoFullName string, prNumber int32) (sqlcgen.ReviewVerdict, error) {
	return s.q.GetLatestReviewVerdict(ctx, sqlcgen.GetLatestReviewVerdictParams{
		RepoFullName: repoFullName,
		PrNumber:     prNumber,
	})
}

// ListLatestAutoApproved returns repoFullName's own latest-per-PR
// verdicts, posted after sinceTime, whose Shippable is 'auto' -- bounded
// by limit. See ListLatestAutoApprovedInRepo's own generated doc comment
// for the DISCOVERY-only role this plays for internal/app/automerge.
func (s *ReviewVerdictStore) ListLatestAutoApproved(ctx context.Context, repoFullName string, sinceTime pgtype.Timestamptz, limit int32) ([]sqlcgen.ReviewVerdict, error) {
	return s.q.ListLatestAutoApprovedInRepo(ctx, sqlcgen.ListLatestAutoApprovedInRepoParams{
		RepoFullName: repoFullName,
		CreatedAt:    sinceTime,
		Limit:        limit,
	})
}

// ListInWindow returns every verdict posted for repoFullName after
// sinceTime, oldest first, bounded by limit -- the analytics rollups'
// own shared bounded scan (§21.1).
func (s *ReviewVerdictStore) ListInWindow(ctx context.Context, repoFullName string, sinceTime pgtype.Timestamptz, limit int32) ([]sqlcgen.ReviewVerdict, error) {
	return s.q.ListReviewVerdictsInWindow(ctx, sqlcgen.ListReviewVerdictsInWindowParams{
		RepoFullName: repoFullName,
		CreatedAt:    sinceTime,
		Limit:        limit,
	})
}
