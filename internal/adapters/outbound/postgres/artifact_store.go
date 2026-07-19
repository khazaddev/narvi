package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// ArtifactStore is a thin, pass-through wrapper around the sqlc-generated
// artifacts queries (§4.3, §6.3 GET /api/sessions/:id/artifacts and the
// client WS hub's own SubscribedPayload.artifacts, §6.2). No caching, no
// retries, no business rules. Create is Step 21's ("e2e happy path") own
// addition -- app/sessionactor's own createPRBestEffort (pushpr.go) is its
// first real caller, recording a "pr"-typed artifact once
// ports.SourceControl.CreatePR succeeds; previews (Step 48) and uploads
// (Step 49) still have no Create caller.
type ArtifactStore struct {
	q *sqlcgen.Queries
}

// NewArtifactStore builds an ArtifactStore backed by pool.
func NewArtifactStore(pool *pgxpool.Pool) *ArtifactStore {
	return &ArtifactStore{q: sqlcgen.New(pool)}
}

// WithTx returns an ArtifactStore whose queries run on tx instead of the
// pool this store was built with -- used by app/sessionactor's
// transactional-write helper (§2), the same convention every other store
// in this package already follows.
func (s *ArtifactStore) WithTx(tx pgx.Tx) *ArtifactStore {
	return &ArtifactStore{q: s.q.WithTx(tx)}
}

// Create inserts a new artifact row and returns it.
func (s *ArtifactStore) Create(ctx context.Context, arg sqlcgen.CreateArtifactParams) (sqlcgen.Artifact, error) {
	return s.q.CreateArtifact(ctx, arg)
}

// ListForSession returns every artifact row for sessionID, oldest first.
// Unbounded (no limit/cursor) is deliberate: this list is expected to stay
// small (§6.2's own design-decision note).
func (s *ArtifactStore) ListForSession(ctx context.Context, sessionID pgtype.UUID) ([]sqlcgen.Artifact, error) {
	return s.q.ListArtifactsForSession(ctx, sessionID)
}
