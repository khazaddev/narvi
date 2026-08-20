package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// DigestChannelStore is a thin, pass-through wrapper around the
// sqlc-generated channel-discovery queries (§21.3) -- see
// queries/digestchannels.sql's own doc comment for the full "reusing
// existing session-thread association tables" design.
type DigestChannelStore struct {
	q *sqlcgen.Queries
}

// NewDigestChannelStore builds a DigestChannelStore backed by pool.
func NewDigestChannelStore(pool *pgxpool.Pool) *DigestChannelStore {
	return &DigestChannelStore{q: sqlcgen.New(pool)}
}

// ListSlackChannels returns every distinct Slack channel_id repoFullName's
// own review sessions have threaded through since sinceTime.
func (s *DigestChannelStore) ListSlackChannels(ctx context.Context, repoFullName string, sinceTime pgtype.Timestamptz) ([]string, error) {
	return s.q.ListSlackChannelsForRepoSince(ctx, sqlcgen.ListSlackChannelsForRepoSinceParams{
		RepoFullName: repoFullName,
		CreatedAt:    sinceTime,
	})
}

// ListLinearOrganizations returns every distinct Linear organization_id
// repoFullName's own review sessions have threaded through since
// sinceTime.
func (s *DigestChannelStore) ListLinearOrganizations(ctx context.Context, repoFullName string, sinceTime pgtype.Timestamptz) ([]string, error) {
	return s.q.ListLinearOrganizationsForRepoSince(ctx, sqlcgen.ListLinearOrganizationsForRepoSinceParams{
		RepoFullName: repoFullName,
		CreatedAt:    sinceTime,
	})
}

// ListDistinctRepos returns every distinct repo_full_name github_pr_sessions
// has claimed since sinceTime, bounded by limit.
func (s *DigestChannelStore) ListDistinctRepos(ctx context.Context, sinceTime pgtype.Timestamptz, limit int32) ([]string, error) {
	return s.q.ListDistinctReposWithRecentSessions(ctx, sqlcgen.ListDistinctReposWithRecentSessionsParams{
		ClaimedAt: sinceTime,
		Limit:     limit,
	})
}
