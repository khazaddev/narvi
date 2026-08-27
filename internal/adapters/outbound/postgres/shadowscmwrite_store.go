package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// ShadowSCMWriteStore is the append-only ledger of suppressed SCM writes
// (§30.6). It has a Create and a List and deliberately no update or delete:
// a suppressed effect is a historical fact, and there is nothing to correct
// about the record of something that never happened.
type ShadowSCMWriteStore struct {
	q *sqlcgen.Queries
}

// NewShadowSCMWriteStore builds a store over pool.
func NewShadowSCMWriteStore(pool *pgxpool.Pool) *ShadowSCMWriteStore {
	return &ShadowSCMWriteStore{q: sqlcgen.New(pool)}
}

// WithTx returns a store whose queries run on tx.
//
// This matters more here than for most stores: §30.6's record-or-fail
// semantics mean the ledger insert and whatever transaction the caller is
// already inside must succeed or fail together. A gate that recorded
// outside the caller's transaction could report a suppression the caller
// then rolled back.
func (s *ShadowSCMWriteStore) WithTx(tx pgx.Tx) *ShadowSCMWriteStore {
	return &ShadowSCMWriteStore{q: s.q.WithTx(tx)}
}

// Create appends one suppressed write.
func (s *ShadowSCMWriteStore) Create(ctx context.Context, arg sqlcgen.CreateShadowSCMWriteParams) (sqlcgen.ShadowScmWrite, error) {
	return s.q.CreateShadowSCMWrite(ctx, arg)
}

// ListForRepo returns repoFullName's suppressed writes, newest first.
func (s *ShadowSCMWriteStore) ListForRepo(ctx context.Context, repoFullName string, limit int32) ([]sqlcgen.ShadowScmWrite, error) {
	return s.q.ListShadowSCMWritesForRepo(ctx, sqlcgen.ListShadowSCMWritesForRepoParams{
		RepoFullName: repoFullName,
		Limit:        limit,
	})
}
