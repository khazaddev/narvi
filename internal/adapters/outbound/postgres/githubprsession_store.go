package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// GitHubPRSessionStore is a thin, pass-through wrapper around the
// sqlc-generated github_pr_sessions queries (§8.2's "atomic claim
// coalescing of concurrent @mentions", Step 32 "GitHub ingress"). No
// caching, no retries, no business rules -- the coalescing DECISION
// (create a new session vs. reuse an existing one) lives in
// internal/adapters/inbound/github/coalesce.go, which is the only caller
// today.
type GitHubPRSessionStore struct {
	q *sqlcgen.Queries
}

// NewGitHubPRSessionStore builds a GitHubPRSessionStore backed by pool.
func NewGitHubPRSessionStore(pool *pgxpool.Pool) *GitHubPRSessionStore {
	return &GitHubPRSessionStore{q: sqlcgen.New(pool)}
}

// WithTx returns a GitHubPRSessionStore whose queries run on tx instead of
// the pool this store was built with -- EnsureRow/LockForUpdate/
// SetSessionID must ALL run in the SAME transaction for the atomic claim
// to be sound (see migrations/000028_github_pr_sessions.up.sql's own doc
// comment), so every real caller uses WithTx, never the bare pool-backed
// store this constructor returns directly.
func (s *GitHubPRSessionStore) WithTx(tx pgx.Tx) *GitHubPRSessionStore {
	return &GitHubPRSessionStore{q: s.q.WithTx(tx)}
}

// EnsureRow idempotently ensures a (repoFullName, prNumber) claim row
// exists, with session_id left NULL on a fresh insert -- see
// EnsureGitHubPRSessionRow's own generated doc comment.
func (s *GitHubPRSessionStore) EnsureRow(ctx context.Context, repoFullName string, prNumber int32) error {
	return s.q.EnsureGitHubPRSessionRow(ctx, sqlcgen.EnsureGitHubPRSessionRowParams{
		RepoFullName: repoFullName,
		PrNumber:     prNumber,
	})
}

// LockForUpdate locks the (repoFullName, prNumber) claim row for the rest
// of the caller's own transaction, returning its CURRENT session_id
// (Valid == false means no session has claimed this PR yet -- the caller
// is the first mention and should create one). See
// LockGitHubPRSessionForUpdate's own generated doc comment for the
// concurrency-serialization this provides.
func (s *GitHubPRSessionStore) LockForUpdate(ctx context.Context, repoFullName string, prNumber int32) (pgtype.UUID, error) {
	return s.q.LockGitHubPRSessionForUpdate(ctx, sqlcgen.LockGitHubPRSessionForUpdateParams{
		RepoFullName: repoFullName,
		PrNumber:     prNumber,
	})
}

// SetSessionID fills in session_id for a (repoFullName, prNumber) claim
// row still holding LockForUpdate's own row lock -- called exactly once,
// by whichever caller observed session_id NULL under that lock.
func (s *GitHubPRSessionStore) SetSessionID(ctx context.Context, repoFullName string, prNumber int32, sessionID pgtype.UUID) error {
	return s.q.SetGitHubPRSessionID(ctx, sqlcgen.SetGitHubPRSessionIDParams{
		RepoFullName: repoFullName,
		PrNumber:     prNumber,
		SessionID:    sessionID,
	})
}

// GetBySessionID is the REVERSE lookup Step 35 ("outbox delivery") needs:
// given a session_id, which (repoFullName, prNumber) PR does it back?
// Returns pgx.ErrNoRows (unwrapped) when sessionID was never created via a
// GitHub PR mention.
func (s *GitHubPRSessionStore) GetBySessionID(ctx context.Context, sessionID pgtype.UUID) (sqlcgen.GithubPrSession, error) {
	return s.q.GetGitHubPRSessionBySessionID(ctx, sessionID)
}
