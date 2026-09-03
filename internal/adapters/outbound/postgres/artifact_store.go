package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// ArtifactStore is a thin, pass-through wrapper around the sqlc-generated
// artifacts queries (§4.3, §6.3 GET /api/sessions/:id/artifacts and the
// client WS hub's own SubscribedPayload.artifacts, §6.2). No caching, no
// retries, no business rules. Create is §9.3's ("e2e happy path") own
// addition -- app/sessionactor's own createPRBestEffort (pushpr.go) is its
// first real caller, recording a "pr"-typed artifact once
// ports.SourceControl.CreatePR succeeds; previews (§8.2) and uploads
// (§14.4) still have no Create caller.
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

// CreateUpload inserts a new 'upload'-typed artifact row in status
// 'pending' (§8.6's own mint, §28.4) and returns it.
func (s *ArtifactStore) CreateUpload(ctx context.Context, arg sqlcgen.CreateUploadArtifactParams) (sqlcgen.Artifact, error) {
	return s.q.CreateUploadArtifact(ctx, arg)
}

// GetForSession fetches the artifact row identified by (id, sessionID) --
// used by the confirm and content/download handlers alike (§28.4/§28.5).
// Returns pgx.ErrNoRows (via errors.Is) when id does not exist or belongs
// to a different session -- callers must treat both cases identically
// (§28.5: "404 uploadID unknown, not this session's, or not status='ready'"),
// never leak which one it was.
func (s *ArtifactStore) GetForSession(ctx context.Context, id, sessionID pgtype.UUID) (sqlcgen.Artifact, error) {
	return s.q.GetArtifactForSession(ctx, sqlcgen.GetArtifactForSessionParams{ID: id, SessionID: sessionID})
}

// ListReadyUploadsByIDsForSession backs attachmentIds validation at the
// turn-creation chokepoint (createTurnLocked, §28.5): returns only the
// rows among ids that are 'upload'-typed, status='ready', and belong to
// sessionID -- callers compare the result's ids against the requested set
// to find any unknown/foreign/not-ready id.
func (s *ArtifactStore) ListReadyUploadsByIDsForSession(ctx context.Context, sessionID pgtype.UUID, ids []pgtype.UUID) ([]sqlcgen.Artifact, error) {
	return s.q.ListReadyUploadArtifactsByIDsForSession(ctx, sqlcgen.ListReadyUploadArtifactsByIDsForSessionParams{
		SessionID: sessionID,
		Ids:       ids,
	})
}

// SumSessionUploadBytes returns SUM(size_bytes) over sessionID's own
// pending+ready upload rows (§28.4's own session-quota check, derived from
// rows that already exist rather than a dedicated counter column).
func (s *ArtifactStore) SumSessionUploadBytes(ctx context.Context, sessionID pgtype.UUID) (int64, error) {
	return s.q.SumSessionUploadBytes(ctx, sessionID)
}

// MarkUploadReadyIfPending performs confirm's success transition
// (pending -> ready, §28.4), guarded so a retried confirm of an
// already-resolved row is a no-op: RowsAffected == 1 means this call
// performed the transition; 0 means some other caller (a prior attempt, a
// concurrent retry) already resolved this row first -- the caller re-reads
// via GetForSession to learn the recorded outcome, exactly the pattern
// planapprove.go's own ApprovePlanIfAwaitingApproval established.
func (s *ArtifactStore) MarkUploadReadyIfPending(ctx context.Context, id, sessionID pgtype.UUID) (int64, error) {
	return s.q.MarkUploadArtifactReadyIfPending(ctx, sqlcgen.MarkUploadArtifactReadyIfPendingParams{ID: id, SessionID: sessionID})
}

// MarkUploadFailedIfPending performs confirm's failure transition
// (pending -> failed(reason), §28.4), guarded the same way as
// MarkUploadReadyIfPending above.
func (s *ArtifactStore) MarkUploadFailedIfPending(ctx context.Context, id, sessionID pgtype.UUID, reason sqlcgen.ArtifactFailureReason) (int64, error) {
	return s.q.MarkUploadArtifactFailedIfPending(ctx, sqlcgen.MarkUploadArtifactFailedIfPendingParams{
		ID:            id,
		SessionID:     sessionID,
		FailureReason: &reason,
	})
}

// GetPRArtifactByURL reports whether SOME session pushed and opened the
// pull request at url (§16's own "authored by a platform session"
// signal for ready_to_merge -- see GetPRArtifactByURL's own generated doc
// comment). Returns pgx.ErrNoRows (via errors.Is) when no such artifact
// exists -- callers must never treat that as an error, only as "not
// platform-authored".
func (s *ArtifactStore) GetPRArtifactByURL(ctx context.Context, url string) (sqlcgen.Artifact, error) {
	return s.q.GetPRArtifactByURL(ctx, url)
}

// ListPendingUploadsOlderThan backs the abandonment sweep (§28.4): pending
// upload rows created before cutoff, oldest first, capped at limit rows
// per sweep pass.
func (s *ArtifactStore) ListPendingUploadsOlderThan(ctx context.Context, cutoff pgtype.Timestamptz, limit int32) ([]sqlcgen.Artifact, error) {
	return s.q.ListPendingUploadArtifactsOlderThan(ctx, sqlcgen.ListPendingUploadArtifactsOlderThanParams{
		CreatedAt: cutoff,
		Limit:     limit,
	})
}
