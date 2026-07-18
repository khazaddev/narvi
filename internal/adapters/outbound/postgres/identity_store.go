package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// IdentityStore is a thin, pass-through wrapper around the sqlc-generated
// identities queries (§13.2 identity graph, migrations/000003_identities.
// up.sql + migrations/000017_auth_v1.up.sql's own access_token_encrypted
// column). No caching, no retries, no business rules -- the OAuth
// returning-vs-first-time-sign-in branch and the encrypt-before-store step
// are internal/adapters/inbound/auth's job (Step 20).
type IdentityStore struct {
	q *sqlcgen.Queries
}

// NewIdentityStore builds an IdentityStore backed by pool.
func NewIdentityStore(pool *pgxpool.Pool) *IdentityStore {
	return &IdentityStore{q: sqlcgen.New(pool)}
}

// WithTx returns an IdentityStore whose queries run on tx instead of the
// pool this store was built with -- see UserStore.WithTx's own doc comment;
// both are used together, inside the SAME transaction, by the OAuth
// callback handler's first-time-sign-in path.
func (s *IdentityStore) WithTx(tx pgx.Tx) *IdentityStore {
	return &IdentityStore{q: s.q.WithTx(tx)}
}

// Create inserts a new identities row linking a user to a provider
// identity, and returns it.
func (s *IdentityStore) Create(ctx context.Context, arg sqlcgen.CreateIdentityParams) (sqlcgen.Identity, error) {
	return s.q.CreateIdentity(ctx, arg)
}

// GetByProviderAndExternalID fetches an identities row by its (provider,
// external_id) unique key -- the OAuth callback's own returning-vs-
// first-time-sign-in check (a pgx.ErrNoRows result means "first time").
func (s *IdentityStore) GetByProviderAndExternalID(ctx context.Context, provider sqlcgen.IdentityProvider, externalID string) (sqlcgen.Identity, error) {
	return s.q.GetIdentityByProviderAndExternalID(ctx, sqlcgen.GetIdentityByProviderAndExternalIDParams{
		Provider:   provider,
		ExternalID: externalID,
	})
}

// GetByUserAndProvider fetches a user's identity for one provider (Step 21
// "e2e happy path"'s own scm-credentials endpoint uses this to find a
// session's created_by user's GitHub identity).
func (s *IdentityStore) GetByUserAndProvider(ctx context.Context, userID pgtype.UUID, provider sqlcgen.IdentityProvider) (sqlcgen.Identity, error) {
	return s.q.GetIdentityByUserAndProvider(ctx, sqlcgen.GetIdentityByUserAndProviderParams{
		UserID:   userID,
		Provider: provider,
	})
}

// UpdateAccessToken re-encrypts and stores a fresh provider access token on
// an already-linked identity -- called on every successful sign-in of a
// RETURNING user (GitHub's own classic OAuth tokens don't expire, but
// re-storing on each login is harmless and keeps the stored token fresh if
// the user ever re-authorized with different scopes).
func (s *IdentityStore) UpdateAccessToken(ctx context.Context, arg sqlcgen.UpdateIdentityAccessTokenParams) (sqlcgen.Identity, error) {
	return s.q.UpdateIdentityAccessToken(ctx, arg)
}
