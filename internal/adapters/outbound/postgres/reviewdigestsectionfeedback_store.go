package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// ReviewDigestSectionFeedbackStore is a thin, pass-through wrapper around
// the sqlc-generated review_digest_section_feedback queries (
// "review deep path: adversarial counter-review + readout measurement",
// §26.5) -- see migrations/000086_review_digest_section_feedback.up.sql's
// own doc comment for the table's full design. No caching, no retries, no
// business rules -- mirrors FalsePositivePatternStore's own identical
// "thin wrapper, callers decide what a result means" convention.
type ReviewDigestSectionFeedbackStore struct {
	q *sqlcgen.Queries
}

// NewReviewDigestSectionFeedbackStore builds a
// ReviewDigestSectionFeedbackStore backed by pool.
func NewReviewDigestSectionFeedbackStore(pool *pgxpool.Pool) *ReviewDigestSectionFeedbackStore {
	return &ReviewDigestSectionFeedbackStore{q: sqlcgen.New(pool)}
}

// Upsert creates-or-updates one review_digest_section_feedback row keyed
// on (commentID, commentType) -- see UpsertReviewDigestSectionFeedback's
// own generated doc comment for why the PAIR, not commentID alone, is the
// real idempotency key, and for why every column but the INSERT's own
// initial values is preserved on a redelivered/retried capture of the
// SAME (comment id, comment type) pair. commentType is the exact
// eventType string the caller received the triggering webhook as
// (internal/adapters/inbound/github's own eventTypeIssueComment/
// eventTypePullRequestReviewComment constants). inserted reports whether
// this call captured a genuinely new contest (true) or observed an
// already-known (comment id, comment type) pair (false) -- callers use it
// purely for logging/observability, mirroring FalsePositivePatternStore.
// Upsert's identical Inserted-is-log-only convention.
func (s *ReviewDigestSectionFeedbackStore) Upsert(ctx context.Context, repoFullName string, prNumber int32, section, contentHash, commentType string, commentID int64, reason string, createdBy pgtype.UUID) (row sqlcgen.ReviewDigestSectionFeedback, inserted bool, err error) {
	r, err := s.q.UpsertReviewDigestSectionFeedback(ctx, sqlcgen.UpsertReviewDigestSectionFeedbackParams{
		RepoFullName: repoFullName,
		PrNumber:     prNumber,
		Section:      section,
		ContentHash:  contentHash,
		CommentType:  commentType,
		CommentID:    commentID,
		Reason:       reason,
		CreatedBy:    createdBy,
	})
	if err != nil {
		return sqlcgen.ReviewDigestSectionFeedback{}, false, err
	}
	return sqlcgen.ReviewDigestSectionFeedback{
		ID:           r.ID,
		RepoFullName: r.RepoFullName,
		PrNumber:     r.PrNumber,
		Section:      r.Section,
		ContentHash:  r.ContentHash,
		CommentType:  r.CommentType,
		CommentID:    r.CommentID,
		Reason:       r.Reason,
		CreatedBy:    r.CreatedBy,
		CreatedAt:    r.CreatedAt,
	}, r.Inserted, nil
}

// List returns every contest for repoFullName, optionally narrowed to one
// section (nil means every section), newest first, bounded by limit --
// §26.5's own audit view / contestation-rate KPI source.
func (s *ReviewDigestSectionFeedbackStore) List(ctx context.Context, repoFullName string, section *string, limit int32) ([]sqlcgen.ReviewDigestSectionFeedback, error) {
	return s.q.ListReviewDigestSectionFeedback(ctx, sqlcgen.ListReviewDigestSectionFeedbackParams{
		RepoFullName: repoFullName,
		Section:      section,
		Limit:        limit,
	})
}

// Count returns how many contests exist for (repoFullName, section) since
// sinceTime -- the contestation-rate KPI's own numerator (§26.5).
func (s *ReviewDigestSectionFeedbackStore) Count(ctx context.Context, repoFullName, section string, sinceTime pgtype.Timestamptz) (int64, error) {
	return s.q.CountReviewDigestSectionFeedback(ctx, sqlcgen.CountReviewDigestSectionFeedbackParams{
		RepoFullName: repoFullName,
		Section:      section,
		CreatedAt:    sinceTime,
	})
}
