package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// LinearAgentSessionStore is a thin, pass-through wrapper around the
// sqlc-generated linear_agent_sessions queries (Step 34, "Linear
// ingress", §8.10 -- see migrations/000030_linear_agent_sessions.up.sql's
// own doc comment for why this table exists and how its atomic claim is
// used). No caching, no retries, no business rules -- internal/adapters/
// inbound/linear's own webhook handler is the only caller.
type LinearAgentSessionStore struct {
	q *sqlcgen.Queries
}

// NewLinearAgentSessionStore builds a LinearAgentSessionStore backed by pool.
func NewLinearAgentSessionStore(pool *pgxpool.Pool) *LinearAgentSessionStore {
	return &LinearAgentSessionStore{q: sqlcgen.New(pool)}
}

// WithTx returns a LinearAgentSessionStore whose queries run on tx instead
// of the pool this store was built with -- same convention as every other
// store in this package.
func (s *LinearAgentSessionStore) WithTx(tx pgx.Tx) *LinearAgentSessionStore {
	return &LinearAgentSessionStore{q: s.q.WithTx(tx)}
}

// Claim attempts an atomic first-writer-wins claim on agentSessionID (see
// ClaimLinearAgentSession's own doc comment for the full race this
// closes). Row.Inserted reports whether THIS call is the one that owns
// creating the backing Narvi session.
func (s *LinearAgentSessionStore) Claim(ctx context.Context, agentSessionID, organizationID string) (sqlcgen.ClaimLinearAgentSessionRow, error) {
	return s.q.ClaimLinearAgentSession(ctx, sqlcgen.ClaimLinearAgentSessionParams{
		AgentSessionID: agentSessionID,
		OrganizationID: organizationID,
	})
}

// SetSessionID attaches the real Narvi session id to a row this same
// request just won the claim on -- called only after createSessionCore's
// own transaction has already committed.
func (s *LinearAgentSessionStore) SetSessionID(ctx context.Context, agentSessionID string, sessionID pgtype.UUID) error {
	return s.q.SetLinearAgentSessionSessionID(ctx, sqlcgen.SetLinearAgentSessionSessionIDParams{
		AgentSessionID: agentSessionID,
		SessionID:      sessionID,
	})
}

// GetByAgentSessionID looks up the Narvi session (if any) already backing
// agentSessionID -- the `prompted`-event routing lookup. Returns
// pgx.ErrNoRows (via the standard errors.Is check) when no mapping exists
// at all; callers must ALSO check Row.SessionID.Valid, since a row can
// exist with a still-NULL session_id while its own `created` claim is
// in flight.
func (s *LinearAgentSessionStore) GetByAgentSessionID(ctx context.Context, agentSessionID string) (sqlcgen.LinearAgentSession, error) {
	row, err := s.q.GetLinearAgentSessionByAgentSessionID(ctx, agentSessionID)
	if err != nil {
		return sqlcgen.LinearAgentSession{}, err
	}
	return row, nil
}

// GetBySessionID is the REVERSE lookup Step 35 ("outbox delivery") needs:
// given a session_id, which agent_session_id/organization_id does it
// back? Returns pgx.ErrNoRows (unwrapped) when sessionID was never
// created via a Linear agent session.
func (s *LinearAgentSessionStore) GetBySessionID(ctx context.Context, sessionID pgtype.UUID) (sqlcgen.LinearAgentSession, error) {
	return s.q.GetLinearAgentSessionBySessionID(ctx, sessionID)
}
