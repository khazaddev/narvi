package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// AutomationStore is a thin, pass-through wrapper around the sqlc-generated
// automations queries (Step 51, "automations: engine", §3.5). No caching,
// no business rules -- EvaluateFailureStrike/Transition (internal/domain/
// automation) are pure and live there; the CAS-guarded claim-and-record
// loop lives in app/automation.
type AutomationStore struct {
	q *sqlcgen.Queries
}

// NewAutomationStore builds an AutomationStore backed by pool.
func NewAutomationStore(pool *pgxpool.Pool) *AutomationStore {
	return &AutomationStore{q: sqlcgen.New(pool)}
}

// WithTx returns an AutomationStore whose queries run on tx instead of the
// pool this store was built with -- LockForUpdate/ApplyFailureStrike MUST
// be called on a WithTx-scoped store, inside the SAME transaction as
// automation_invocations' own MarkFailureCounted (see this package's own
// AutomationInvocationStore), mirroring every other store's identical
// WithTx convention.
func (s *AutomationStore) WithTx(tx pgx.Tx) *AutomationStore {
	return &AutomationStore{q: s.q.WithTx(tx)}
}

// Create inserts a brand-new automations row -- no HTTP caller exists yet
// in this Step (Step 52/76 own that surface); used directly by this
// package's own integration tests.
func (s *AutomationStore) Create(ctx context.Context, arg sqlcgen.CreateAutomationParams) (sqlcgen.Automation, error) {
	return s.q.CreateAutomation(ctx, arg)
}

// Get fetches the automations row for id, or pgx.ErrNoRows if none exists.
func (s *AutomationStore) Get(ctx context.Context, id pgtype.UUID) (sqlcgen.Automation, error) {
	return s.q.GetAutomation(ctx, id)
}

// LockForUpdate reads and row-locks the automations row for id -- callers
// MUST be inside an open transaction (WithTx) and MUST subsequently call
// either ApplyFailureStrike or nothing before committing/rolling back; see
// LockAutomationForUpdate's own generated doc comment for the concurrent-
// invocation-closure race this lock closes.
func (s *AutomationStore) LockForUpdate(ctx context.Context, id pgtype.UUID) (sqlcgen.Automation, error) {
	return s.q.LockAutomationForUpdate(ctx, id)
}

// ApplyFailureStrike records automation.EvaluateFailureStrike's own
// verdict -- callers MUST already hold LockForUpdate's own row lock, in
// the SAME transaction.
func (s *AutomationStore) ApplyFailureStrike(ctx context.Context, arg sqlcgen.ApplyFailureStrikeParams) (sqlcgen.Automation, error) {
	return s.q.ApplyFailureStrike(ctx, arg)
}

// ResetConsecutiveFailures resets id's own consecutive-failure streak to
// zero -- idempotent, no CAS guard needed (see ResetConsecutiveFailures's
// own generated doc comment).
func (s *AutomationStore) ResetConsecutiveFailures(ctx context.Context, id pgtype.UUID) (int64, error) {
	return s.q.ResetConsecutiveFailures(ctx, id)
}

// Resume applies automation.TriggerResume: Paused -> Active, resetting the
// consecutive-failure streak. No HTTP caller exists yet in this Step (Step
// 52/76 own the actual "Resume" button) -- reserved so that surface needs
// no store-layer change to use it. pgx.ErrNoRows means id is not currently
// Paused (a no-op, not an error the caller should surface as one).
func (s *AutomationStore) Resume(ctx context.Context, id pgtype.UUID) (sqlcgen.Automation, error) {
	return s.q.ResumeAutomation(ctx, id)
}
