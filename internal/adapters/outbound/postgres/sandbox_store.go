package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// SandboxStore is a thin, pass-through wrapper around the sqlc-generated
// sandbox queries (§4.3 SandboxStore). No caching, no retries, no business
// rules — that lives in domain/sandbox (PR-07) and app/sessionactor
// (PR-11+).
type SandboxStore struct {
	q *sqlcgen.Queries
}

// NewSandboxStore builds a SandboxStore backed by pool.
func NewSandboxStore(pool *pgxpool.Pool) *SandboxStore {
	return &SandboxStore{q: sqlcgen.New(pool)}
}

// Create inserts a new sandbox row for sessionID and returns it. The
// database enforces one sandbox row per session via UNIQUE(session_id)
// (§3.2).
func (s *SandboxStore) Create(ctx context.Context, sessionID pgtype.UUID) (sqlcgen.Sandbox, error) {
	return s.q.CreateSandbox(ctx, sessionID)
}

// Get fetches the sandbox row for sessionID.
func (s *SandboxStore) Get(ctx context.Context, sessionID pgtype.UUID) (sqlcgen.Sandbox, error) {
	return s.q.GetSandbox(ctx, sessionID)
}
