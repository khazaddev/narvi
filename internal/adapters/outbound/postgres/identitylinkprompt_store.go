package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// IdentityLinkPromptStore is a thin, pass-through wrapper around the
// sqlc-generated identity_link_prompts queries (§13.2's own "pending
// links" table, migrations/000036_identity_link_prompts.up.sql). No
// caching, no retries, no business rules -- the auto-link decision itself
// (whether to create a prompt at all, whether to reuse an existing one)
// is internal/app/identitylink's job; this store only ever persists what
// it's given.
type IdentityLinkPromptStore struct {
	q *sqlcgen.Queries
}

// NewIdentityLinkPromptStore builds an IdentityLinkPromptStore backed by
// pool.
func NewIdentityLinkPromptStore(pool *pgxpool.Pool) *IdentityLinkPromptStore {
	return &IdentityLinkPromptStore{q: sqlcgen.New(pool)}
}

// WithTx returns an IdentityLinkPromptStore whose queries run on tx
// instead of the pool this store was built with -- used by internal/app/
// identitylink.Resolve's own auto-link transaction and by the magic-link
// consume handler's own link-then-delete-prompt transaction.
func (s *IdentityLinkPromptStore) WithTx(tx pgx.Tx) *IdentityLinkPromptStore {
	return &IdentityLinkPromptStore{q: s.q.WithTx(tx)}
}

// Create inserts a new identity_link_prompts row and returns it.
func (s *IdentityLinkPromptStore) Create(ctx context.Context, arg sqlcgen.CreateIdentityLinkPromptParams) (sqlcgen.IdentityLinkPrompt, error) {
	return s.q.CreateIdentityLinkPrompt(ctx, arg)
}

// GetByNonceHash fetches a prompt by its nonce_hash -- the magic-link
// consume handler's own lookup. pgx.ErrNoRows means the presented nonce
// is wrong, already consumed, or never existed.
func (s *IdentityLinkPromptStore) GetByNonceHash(ctx context.Context, nonceHash string) (sqlcgen.IdentityLinkPrompt, error) {
	return s.q.GetIdentityLinkPromptByNonceHash(ctx, nonceHash)
}

// GetLatestForProviderAndExternalID fetches the most recently created
// prompt for (provider, externalID), if any -- Resolve's own re-entry
// check (see queries/identity_link_prompts.sql's own doc comment).
// pgx.ErrNoRows means no prompt has ever been created for this identity.
func (s *IdentityLinkPromptStore) GetLatestForProviderAndExternalID(ctx context.Context, provider sqlcgen.IdentityProvider, externalID string) (sqlcgen.IdentityLinkPrompt, error) {
	return s.q.GetLatestLinkPromptForProviderAndExternalID(ctx, sqlcgen.GetLatestLinkPromptForProviderAndExternalIDParams{
		Provider:   provider,
		ExternalID: externalID,
	})
}

// List returns every still-present prompt, newest first -- backs the
// members API's own overview endpoint (§13.2's "pending-link state").
func (s *IdentityLinkPromptStore) List(ctx context.Context) ([]sqlcgen.IdentityLinkPrompt, error) {
	return s.q.ListLinkPrompts(ctx)
}

// Delete removes one prompt by id -- called once the magic link it backs
// has been successfully consumed.
func (s *IdentityLinkPromptStore) Delete(ctx context.Context, id pgtype.UUID) error {
	return s.q.DeleteIdentityLinkPrompt(ctx, id)
}

// DeleteForProviderAndExternalID removes EVERY prompt for (provider,
// externalID) -- called once that identity is genuinely linked (by
// whichever path won: the magic-link click itself, a later auto-link, or
// an admin force-link), so no other still-pending prompt for the SAME
// identity can be clicked afterward.
func (s *IdentityLinkPromptStore) DeleteForProviderAndExternalID(ctx context.Context, provider sqlcgen.IdentityProvider, externalID string) error {
	return s.q.DeleteLinkPromptsForProviderAndExternalID(ctx, sqlcgen.DeleteLinkPromptsForProviderAndExternalIDParams{
		Provider:   provider,
		ExternalID: externalID,
	})
}
