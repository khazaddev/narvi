package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// ImageBuildStore is a thin, pass-through wrapper around the sqlc-generated
// image_builds queries (Step 26, "image builds", §8.5-note/§10-P2). No
// caching, no retries, no business rules -- fingerprinting lives in
// domain/imagebuild, the spawn-time lookup/best-effort-upsert lives in
// app/sessionactor/dispatch.go, and the claim/attempt/record loop lives in
// app/imagebuild.
type ImageBuildStore struct {
	q *sqlcgen.Queries
}

// NewImageBuildStore builds an ImageBuildStore backed by pool.
func NewImageBuildStore(pool *pgxpool.Pool) *ImageBuildStore {
	return &ImageBuildStore{q: sqlcgen.New(pool)}
}

// WithTx returns an ImageBuildStore whose queries run on tx instead of the
// pool this store was built with -- used by app/imagebuild's own claim step
// (a real Postgres transaction, exactly like app/sessionactor's transact
// and app/reconciler's claimDueTimers-style precedent).
func (s *ImageBuildStore) WithTx(tx pgx.Tx) *ImageBuildStore {
	return &ImageBuildStore{q: s.q.WithTx(tx)}
}

// Get fetches the image_builds row for fingerprint, or pgx.ErrNoRows if
// none exists yet.
func (s *ImageBuildStore) Get(ctx context.Context, fingerprint string) (sqlcgen.ImageBuild, error) {
	return s.q.GetImageBuild(ctx, fingerprint)
}

// UpsertPending best-effort inserts a fresh 'pending' tracking row for
// arg.Fingerprint -- a no-op (ON CONFLICT DO NOTHING) if one already
// exists under any status, see UpsertPendingImageBuild's own generated doc
// comment for why that's correct, not merely convenient.
func (s *ImageBuildStore) UpsertPending(ctx context.Context, arg sqlcgen.UpsertPendingImageBuildParams) error {
	return s.q.UpsertPendingImageBuild(ctx, arg)
}

// ListDue returns up to limit rows eligible to (re)attempt right now
// (pending, or failed with an elapsed next_retry_at), locked FOR UPDATE
// SKIP LOCKED -- callers MUST run this inside the same transaction that
// subsequently calls Claim on each returned row (see ListDueImageBuilds's
// own generated doc comment).
func (s *ImageBuildStore) ListDue(ctx context.Context, limit int32) ([]sqlcgen.ImageBuild, error) {
	return s.q.ListDueImageBuilds(ctx, limit)
}

// Claim flips fingerprint's row to 'building' and bumps attempt_count/
// last_attempt_at -- the commit-before-the-real-BuildImage-call half of
// app/imagebuild's own two-step (claim, then attempt outside any
// transaction) shape.
func (s *ImageBuildStore) Claim(ctx context.Context, fingerprint string) (sqlcgen.ImageBuild, error) {
	return s.q.ClaimImageBuild(ctx, fingerprint)
}

// RecordSuccess records a successful BuildImage call: status='ready',
// image_ref set, next_retry_at cleared. Returns pgx.ErrNoRows if
// fingerprint's row is no longer 'building' (an already-superseded/stale
// outcome -- see RecordImageBuildSuccess's own generated doc comment).
func (s *ImageBuildStore) RecordSuccess(ctx context.Context, arg sqlcgen.RecordImageBuildSuccessParams) (sqlcgen.ImageBuild, error) {
	return s.q.RecordImageBuildSuccess(ctx, arg)
}

// RecordFailure records a failed BuildImage call: status='failed',
// next_retry_at set to the caller's own domain/imagebuild.EvaluateBackoff
// decision. Returns pgx.ErrNoRows if fingerprint's row is no longer
// 'building', mirroring RecordSuccess's own identical guard.
func (s *ImageBuildStore) RecordFailure(ctx context.Context, arg sqlcgen.RecordImageBuildFailureParams) (sqlcgen.ImageBuild, error) {
	return s.q.RecordImageBuildFailure(ctx, arg)
}
