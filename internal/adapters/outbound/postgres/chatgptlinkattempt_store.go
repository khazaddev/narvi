package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// ChatGPTLinkAttemptStore is a thin, pass-through wrapper around the
// sqlc-generated chatgpt_link_attempts queries ("models", §29.3,
// migrations/000062_chatgpt_oauth_credentials.up.sql) -- the ChatGPT-
// account-OAuth link flow's own short-lived device-code nonce table,
// mirroring IdentityLinkPromptStore's own "no caching, no retries, no
// business rules" precedent exactly. internal/app/chatgptlink owns every
// actual link/relink/poll decision; this store only ever persists what
// it's given.
type ChatGPTLinkAttemptStore struct {
	q *sqlcgen.Queries
}

// NewChatGPTLinkAttemptStore builds a ChatGPTLinkAttemptStore backed by
// pool.
func NewChatGPTLinkAttemptStore(pool *pgxpool.Pool) *ChatGPTLinkAttemptStore {
	return &ChatGPTLinkAttemptStore{q: sqlcgen.New(pool)}
}

// WithTx returns a ChatGPTLinkAttemptStore whose queries run on tx instead
// of the pool this store was built with -- used by internal/app/
// chatgptlink's own link-success transaction (which also writes/updates a
// provider_credentials row in the SAME transaction).
func (s *ChatGPTLinkAttemptStore) WithTx(tx pgx.Tx) *ChatGPTLinkAttemptStore {
	return &ChatGPTLinkAttemptStore{q: s.q.WithTx(tx)}
}

// Create inserts a new chatgpt_link_attempts row and returns it.
// intervalSeconds is StartDeviceAuth's own server-provided poll interval
// (§29.2), persisted verbatim so PollLink can later throttle against the
// real value rather than a Narvi-side guess.
func (s *ChatGPTLinkAttemptStore) Create(ctx context.Context, userID pgtype.UUID, deviceAuthID, userCode string, intervalSeconds int32, expiresAt time.Time) (sqlcgen.ChatgptLinkAttempt, error) {
	return s.q.CreateChatGPTLinkAttempt(ctx, sqlcgen.CreateChatGPTLinkAttemptParams{
		UserID:          userID,
		DeviceAuthID:    deviceAuthID,
		UserCode:        userCode,
		IntervalSeconds: intervalSeconds,
		ExpiresAt:       pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
}

// GetLatestForUser fetches the most recently created attempt for userID,
// if any -- StartLink's own re-entry check and PollLink's own "find the
// current attempt" lookup (mirrors IdentityLinkPromptStore.
// GetLatestForProviderAndExternalID exactly). pgx.ErrNoRows means no
// attempt has ever been created for this user (or none survived a prior
// DeleteForUser).
func (s *ChatGPTLinkAttemptStore) GetLatestForUser(ctx context.Context, userID pgtype.UUID) (sqlcgen.ChatgptLinkAttempt, error) {
	return s.q.GetLatestChatGPTLinkAttemptForUser(ctx, userID)
}

// UpdateLastPolledAt records a fresh poll timestamp for id -- PollLink's
// own throttle bookkeeping (§29.3 point 2), called immediately before
// actually polling upstream, never after (so a crash mid-poll still
// counts against the throttle rather than allowing an immediate retry
// storm).
func (s *ChatGPTLinkAttemptStore) UpdateLastPolledAt(ctx context.Context, id pgtype.UUID, lastPolledAt time.Time) (sqlcgen.ChatgptLinkAttempt, error) {
	return s.q.UpdateChatGPTLinkAttemptLastPolledAt(ctx, sqlcgen.UpdateChatGPTLinkAttemptLastPolledAtParams{
		ID:           id,
		LastPolledAt: pgtype.Timestamptz{Time: lastPolledAt, Valid: true},
	})
}

// Delete removes one attempt by id -- called once its own device grant
// succeeds or is explicitly abandoned.
func (s *ChatGPTLinkAttemptStore) Delete(ctx context.Context, id pgtype.UUID) error {
	return s.q.DeleteChatGPTLinkAttempt(ctx, id)
}

// DeleteForUser removes EVERY attempt for userID -- called once that
// user's link genuinely succeeds, so no other still-pending attempt for
// the SAME user can be polled to a stale conclusion afterward (mirrors
// IdentityLinkPromptStore.DeleteForProviderAndExternalID exactly).
func (s *ChatGPTLinkAttemptStore) DeleteForUser(ctx context.Context, userID pgtype.UUID) error {
	return s.q.DeleteChatGPTLinkAttemptsForUser(ctx, userID)
}
