package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// ArtifactStore is a thin, pass-through wrapper around the sqlc-generated
// artifacts queries (§4.3, §6.3 GET /api/sessions/:id/artifacts and the
// client WS hub's own SubscribedPayload.artifacts, §6.2). No caching, no
// retries, no business rules. ListForSession only -- nothing in this
// codebase produces an artifact row yet (real artifact CREATION is a
// later Step: PR creation is Step 21+, previews Step 48, uploads Step
// 49), so no Create method exists here either; a later Step adds one when
// something actually mints an artifact.
type ArtifactStore struct {
	q *sqlcgen.Queries
}

// NewArtifactStore builds an ArtifactStore backed by pool.
func NewArtifactStore(pool *pgxpool.Pool) *ArtifactStore {
	return &ArtifactStore{q: sqlcgen.New(pool)}
}

// ListForSession returns every artifact row for sessionID, oldest first.
// Unbounded (no limit/cursor) is deliberate: this list is expected to stay
// small (§6.2's own design-decision note).
func (s *ArtifactStore) ListForSession(ctx context.Context, sessionID pgtype.UUID) ([]sqlcgen.Artifact, error) {
	return s.q.ListArtifactsForSession(ctx, sessionID)
}
