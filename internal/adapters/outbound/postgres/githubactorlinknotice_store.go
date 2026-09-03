package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// GitHubActorLinkNoticeStore is a thin, pass-through wrapper around the
// sqlc-generated github_actor_link_notices queries (batch fix/deny-
// unlinked-github-actors' own anti-spam dedupe table,
// migrations/000043_github_actor_link_notices.up.sql). No caching, no
// TTL comparison, no business rules -- exactly like IdentityLinkPromptStore
// (identitylinkprompt_store.go), the caller (internal/adapters/inbound/
// github's handler.go) owns the "is this still within the TTL window"
// decision; this store only ever persists/reads what it's given.
type GitHubActorLinkNoticeStore struct {
	q *sqlcgen.Queries
}

// NewGitHubActorLinkNoticeStore builds a GitHubActorLinkNoticeStore backed
// by pool.
func NewGitHubActorLinkNoticeStore(pool *pgxpool.Pool) *GitHubActorLinkNoticeStore {
	return &GitHubActorLinkNoticeStore{q: sqlcgen.New(pool)}
}

// Get fetches the notice row for (repoFullName, prNumber, commenterID), if
// any -- the handler's own re-entry check before deciding whether to post
// another "please sign in" reply. pgx.ErrNoRows means this exact
// commenter has never been notified on this exact PR before.
func (s *GitHubActorLinkNoticeStore) Get(ctx context.Context, repoFullName string, prNumber int32, commenterID int64) (sqlcgen.GithubActorLinkNotice, error) {
	return s.q.GetGitHubActorLinkNotice(ctx, sqlcgen.GetGitHubActorLinkNoticeParams{
		RepoFullName: repoFullName,
		PrNumber:     prNumber,
		CommenterID:  commenterID,
	})
}

// Upsert records (or refreshes) notified_at = now() for (repoFullName,
// prNumber, commenterID) -- called immediately after the handler actually
// posts a fresh "please sign in" reply, never speculatively before.
//
// Kept as a low-level primitive alongside Get above (both still exercised
// directly by githubactorlinknotice_store_integration_test.go), but
// actornotauthorizedreply.go's own notify workflow no longer calls this --
// see Claim below for why a separate Get-then-later-Upsert pair, with a
// real GitHub API call in between, is not safe for that job.
func (s *GitHubActorLinkNoticeStore) Upsert(ctx context.Context, repoFullName string, prNumber int32, commenterID int64) (sqlcgen.GithubActorLinkNotice, error) {
	return s.q.UpsertGitHubActorLinkNotice(ctx, sqlcgen.UpsertGitHubActorLinkNoticeParams{
		RepoFullName: repoFullName,
		PrNumber:     prNumber,
		CommenterID:  commenterID,
	})
}

// Claim atomically decides whether THIS call is the one responsible for
// posting a fresh "please sign in" reply for (repoFullName, prNumber,
// commenterID) right now, recording that decision in the SAME statement
// as the check -- see ClaimGitHubActorLinkNotice's own doc comment
// (queries/github_actor_link_notices.sql) for the full "why not Get-then-
// later-Upsert" reasoning this closes. pgx.ErrNoRows means someone else
// (a concurrent delivery, or an earlier mention still within ttl) already
// claimed this notice -- the caller must not post again.
func (s *GitHubActorLinkNoticeStore) Claim(ctx context.Context, repoFullName string, prNumber int32, commenterID int64, ttl time.Duration) (sqlcgen.ClaimGitHubActorLinkNoticeRow, error) {
	return s.q.ClaimGitHubActorLinkNotice(ctx, sqlcgen.ClaimGitHubActorLinkNoticeParams{
		RepoFullName: repoFullName,
		PrNumber:     prNumber,
		CommenterID:  commenterID,
		TtlSeconds:   ttl.Seconds(),
	})
}
