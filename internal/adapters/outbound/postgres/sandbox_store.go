package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// SandboxStore is a thin, pass-through wrapper around the sqlc-generated
// sandbox queries (§4.3 SandboxStore). No caching, no retries, no business
// rules — that lives in domain/sandbox (Step 07) and app/sessionactor
// (Step 11+).
type SandboxStore struct {
	q *sqlcgen.Queries
}

// NewSandboxStore builds a SandboxStore backed by pool.
func NewSandboxStore(pool *pgxpool.Pool) *SandboxStore {
	return &SandboxStore{q: sqlcgen.New(pool)}
}

// WithTx returns a SandboxStore whose queries run on tx instead of the
// pool this store was built with — used by app/sessionactor's
// transactional-write helper (§2).
func (s *SandboxStore) WithTx(tx pgx.Tx) *SandboxStore {
	return &SandboxStore{q: s.q.WithTx(tx)}
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

// UpdateStatus sets a sandbox's status, plus last_seen_at when the caller
// supplies a real timestamp (see UpdateSandboxStatusParams' generated doc
// for the COALESCE semantics).
func (s *SandboxStore) UpdateStatus(ctx context.Context, arg sqlcgen.UpdateSandboxStatusParams) (sqlcgen.Sandbox, error) {
	return s.q.UpdateSandboxStatus(ctx, arg)
}

// UpsertForSpawn creates the sandbox row (if none exists) or bumps its gen
// and resets it to spawning (if one already does) -- see
// UpsertSandboxForSpawnParams' generated doc comment (Step 21, design
// decision 3a).
func (s *SandboxStore) UpsertForSpawn(ctx context.Context, arg sqlcgen.UpsertSandboxForSpawnParams) (sqlcgen.Sandbox, error) {
	return s.q.UpsertSandboxForSpawn(ctx, arg)
}

// UpdateProviderID records the provider's own opaque handle once
// CreateSandbox succeeds.
func (s *SandboxStore) UpdateProviderID(ctx context.Context, arg sqlcgen.UpdateSandboxProviderIDParams) (sqlcgen.Sandbox, error) {
	return s.q.UpdateSandboxProviderID(ctx, arg)
}

// UpdateCircuitBreaker persists internal/domain/sandbox.CircuitBreakerState.
func (s *SandboxStore) UpdateCircuitBreaker(ctx context.Context, arg sqlcgen.UpdateSandboxCircuitBreakerParams) (sqlcgen.Sandbox, error) {
	return s.q.UpdateSandboxCircuitBreaker(ctx, arg)
}

// UpdateSnapshotID records a real, sandbox-confirmed snapshot id once a
// "snapshot_ready" wire event arrives (Step 22, "snapshots & restore",
// design decision 3). Also clears pending_snapshot_message_id back to
// NULL in the same statement -- see UpdateSandboxSnapshotID's own
// generated doc comment.
func (s *SandboxStore) UpdateSnapshotID(ctx context.Context, arg sqlcgen.UpdateSandboxSnapshotIDParams) (sqlcgen.Sandbox, error) {
	return s.q.UpdateSandboxSnapshotID(ctx, arg)
}

// UpdatePendingSnapshotMessageID sets (or clears, via nil) the MessageId
// of whichever Snapshot command this sandbox is currently waiting on a
// snapshot_ready for -- Step 22 fix (message-id correlation), closing a
// real ambiguous-write race an independent review confirmed against a
// real Postgres instance.
func (s *SandboxStore) UpdatePendingSnapshotMessageID(ctx context.Context, arg sqlcgen.UpdateSandboxPendingSnapshotMessageIDParams) (sqlcgen.Sandbox, error) {
	return s.q.UpdateSandboxPendingSnapshotMessageID(ctx, arg)
}
