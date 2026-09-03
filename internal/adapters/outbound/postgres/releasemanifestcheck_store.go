package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// ReleaseManifestCheckStore is a thin, pass-through wrapper around the
// sqlc-generated release_manifest_checks queries (§12.2 item 9, §15.2/
// §15.3) -- see migrations/000097_release_manifest_checks.up.sql's own
// doc comment for the table's full design. No caching, no retries, no
// business rules -- mirrors every other Store in this package.
type ReleaseManifestCheckStore struct {
	q *sqlcgen.Queries
}

// NewReleaseManifestCheckStore builds a ReleaseManifestCheckStore backed
// by pool.
func NewReleaseManifestCheckStore(pool *pgxpool.Pool) *ReleaseManifestCheckStore {
	return &ReleaseManifestCheckStore{q: sqlcgen.New(pool)}
}

// Insert appends one release_manifest_checks row -- internal/app/
// releasereview.Run's own best-effort write, alongside its existing
// outbox-delivered comment.
func (s *ReleaseManifestCheckStore) Insert(ctx context.Context, arg sqlcgen.InsertReleaseManifestCheckParams) (sqlcgen.ReleaseManifestCheck, error) {
	return s.q.InsertReleaseManifestCheck(ctx, arg)
}

// GetLatest fetches (repoFullName, prNumber)'s own most-recently-computed
// check. pgx.ErrNoRows (unwrapped) means no check has ever been persisted
// for this PR -- callers (the release-review readout endpoint) render an
// honest "not yet available" state, never a fabricated one.
func (s *ReleaseManifestCheckStore) GetLatest(ctx context.Context, repoFullName string, prNumber int32) (sqlcgen.ReleaseManifestCheck, error) {
	return s.q.GetLatestReleaseManifestCheck(ctx, sqlcgen.GetLatestReleaseManifestCheckParams{
		RepoFullName: repoFullName,
		PrNumber:     prNumber,
	})
}
