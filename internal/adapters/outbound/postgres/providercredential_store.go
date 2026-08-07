package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// ProviderCredentialStore is a thin, pass-through wrapper around the
// sqlc-generated provider_credentials queries (Step 53, "provider
// credential injection", §25.1/§25.3, migrations/
// 000056_provider_credentials.up.sql) -- this codebase's first generic
// secret-storage table. No caching, no retries, no business rules, and
// (like every store in this package) NO encryption/decryption of its own:
// ValueEncrypted always travels through this store as opaque ciphertext
// bytes -- internal/adapters/inbound/httpapi is the ONLY layer that ever
// calls platform.EncryptToken/DecryptToken over it.
type ProviderCredentialStore struct {
	q *sqlcgen.Queries
}

// NewProviderCredentialStore builds a ProviderCredentialStore backed by
// pool.
func NewProviderCredentialStore(pool *pgxpool.Pool) *ProviderCredentialStore {
	return &ProviderCredentialStore{q: sqlcgen.New(pool)}
}

// Create inserts a new provider_credentials row. scopeTargetID is nil for
// scope=global (enforced by the table's own CHECK constraint on a
// mismatch); a duplicate (scope, scopeTargetID, provider) fails against
// one of the table's own two partial unique indexes -- the caller maps
// that into a 409, never retried here.
func (s *ProviderCredentialStore) Create(ctx context.Context, scope sqlcgen.ProviderCredentialScope, scopeTargetID *string, provider sqlcgen.ProviderCredentialProvider, valueEncrypted []byte) (sqlcgen.ProviderCredential, error) {
	return s.q.CreateProviderCredential(ctx, sqlcgen.CreateProviderCredentialParams{
		Scope:          scope,
		ScopeTargetID:  scopeTargetID,
		Provider:       provider,
		ValueEncrypted: valueEncrypted,
	})
}

// Get fetches one row by id. pgx.ErrNoRows (unwrapped) means no such row.
func (s *ProviderCredentialStore) Get(ctx context.Context, id pgtype.UUID) (sqlcgen.ProviderCredential, error) {
	return s.q.GetProviderCredential(ctx, id)
}

// ListByScope lists every row at exactly one (scope, scopeTargetID) pair
// -- scopeTargetID is nil for scope=global, matching Create's own
// nil-means-global convention.
func (s *ProviderCredentialStore) ListByScope(ctx context.Context, scope sqlcgen.ProviderCredentialScope, scopeTargetID *string) ([]sqlcgen.ProviderCredential, error) {
	return s.q.ListProviderCredentialsByScope(ctx, sqlcgen.ListProviderCredentialsByScopeParams{
		Scope:         scope,
		ScopeTargetID: scopeTargetID,
	})
}

// UpdateValue rotates id's own encrypted value in place -- scope/
// scopeTargetID/provider are immutable once created (see
// httpapi/providercredentials.go's own doc comment for why).
func (s *ProviderCredentialStore) UpdateValue(ctx context.Context, id pgtype.UUID, valueEncrypted []byte) (sqlcgen.ProviderCredential, error) {
	return s.q.UpdateProviderCredentialValue(ctx, sqlcgen.UpdateProviderCredentialValueParams{
		ID:             id,
		ValueEncrypted: valueEncrypted,
	})
}

// Delete removes id's own row, returning the number of rows actually
// deleted (0 or 1) -- the caller renders 404 on 0, matching this
// package's own established "affected-row-count decides the caller's own
// not-found branch" convention.
func (s *ProviderCredentialStore) Delete(ctx context.Context, id pgtype.UUID) (int64, error) {
	return s.q.DeleteProviderCredential(ctx, id)
}

// ListForResolution fetches every candidate row (across all 4 scopes, for
// every provider at once) that could apply to a session naming
// repoFullNames, optionally environmentID, and optionally the session's
// own creator userID (Step 59, §29.4: "resolution keys on sessions.
// created_by") -- the sandbox-facing delivery endpoint's own single read.
// repoFullNames may be empty (never an error); environmentID/userID nil
// mean "this session has no attached Environment"/"this is a bot-
// attributed session with no creator" respectively (matches nothing at
// that scope, never a wildcard).
func (s *ProviderCredentialStore) ListForResolution(ctx context.Context, repoFullNames []string, environmentID, userID *string) ([]sqlcgen.ProviderCredential, error) {
	if repoFullNames == nil {
		repoFullNames = []string{}
	}
	return s.q.ListProviderCredentialsForResolution(ctx, sqlcgen.ListProviderCredentialsForResolutionParams{
		RepoFullNames: repoFullNames,
		EnvironmentID: environmentID,
		UserID:        userID,
	})
}

// UpsertOAuth creates or replaces (§29.3: "relink replaces the row, same
// upsert") the SINGLE scope=user/kind=oauth row for (userID, provider) --
// internal/app/chatgptlink's own link/relink write path (§29.4). Always
// clears any prior oauth_needs_relink back to false, matching a fresh
// link's own "this token is healthy again" reality.
func (s *ProviderCredentialStore) UpsertOAuth(ctx context.Context, userID string, provider sqlcgen.ProviderCredentialProvider, valueEncrypted []byte, oauthExpiresAt time.Time) (sqlcgen.ProviderCredential, error) {
	return s.q.UpsertOAuthProviderCredential(ctx, sqlcgen.UpsertOAuthProviderCredentialParams{
		ScopeTargetID:  &userID,
		Provider:       provider,
		ValueEncrypted: valueEncrypted,
		OauthExpiresAt: pgtype.Timestamptz{Time: oauthExpiresAt, Valid: true},
	})
}

// GetOAuthForUser fetches the scope=user/kind=oauth row for (userID,
// provider), if any -- backs GET/DELETE /api/me/chatgpt-link (§29.3/
// §29.9). pgx.ErrNoRows means "not linked".
func (s *ProviderCredentialStore) GetOAuthForUser(ctx context.Context, userID string, provider sqlcgen.ProviderCredentialProvider) (sqlcgen.ProviderCredential, error) {
	return s.q.GetOAuthProviderCredentialForUser(ctx, sqlcgen.GetOAuthProviderCredentialForUserParams{
		ScopeTargetID: &userID,
		Provider:      provider,
	})
}

// DeleteOAuthForUser removes the scope=user/kind=oauth row for (userID,
// provider) -- DELETE /api/me/chatgpt-link's own unlink (§29.3). Returns
// the number of rows actually deleted (0 or 1), mirroring Delete's own
// "affected-row-count decides the caller's own not-found branch"
// convention.
func (s *ProviderCredentialStore) DeleteOAuthForUser(ctx context.Context, userID string, provider sqlcgen.ProviderCredentialProvider) (int64, error) {
	return s.q.DeleteOAuthProviderCredentialForUser(ctx, sqlcgen.DeleteOAuthProviderCredentialForUserParams{
		ScopeTargetID: &userID,
		Provider:      provider,
	})
}

// ListExpiringOAuth takes a snapshot (FOR UPDATE SKIP LOCKED, see the
// query's own doc comment) of up to limit oauth-kind rows expiring before
// expiresBefore and not already marked oauth_needs_relink -- the refresh
// pump's (internal/app/chatgptrefresh, §29.5) own up-front candidate list
// for one tick, called inside its OWN short transaction that commits
// immediately (S1 fix: see that package's own doc comment/PumpOnce for
// why this is now just a snapshot, not held open across any row's own
// refresh) -- the pump then re-claims and refreshes each candidate one at
// a time via GetExpiringOAuthForUpdate below.
func (s *ProviderCredentialStore) ListExpiringOAuth(ctx context.Context, expiresBefore time.Time, limit int32) ([]sqlcgen.ProviderCredential, error) {
	return s.q.ListExpiringOAuthProviderCredentials(ctx, sqlcgen.ListExpiringOAuthProviderCredentialsParams{
		OauthExpiresAt: pgtype.Timestamptz{Time: expiresBefore, Valid: true},
		Limit:          limit,
	})
}

// GetExpiringOAuthForUpdate re-claims (FOR UPDATE SKIP LOCKED) exactly one
// row by id, re-verifying it still matches ListExpiringOAuth's own due
// criteria -- the refresh pump's own per-row re-claim (S1 fix), called
// inside the SAME short, per-ROW transaction the pump holds open for
// exactly that one row's own refresh+rewrite. pgx.ErrNoRows (unwrapped)
// means id is no longer claimable right now (locked by a concurrent pump
// instance, or no longer due/already needs-relink) -- never a real error;
// the caller simply has nothing to do for id.
func (s *ProviderCredentialStore) GetExpiringOAuthForUpdate(ctx context.Context, id pgtype.UUID, expiresBefore time.Time) (sqlcgen.ProviderCredential, error) {
	return s.q.GetExpiringOAuthProviderCredentialForUpdate(ctx, sqlcgen.GetExpiringOAuthProviderCredentialForUpdateParams{
		ID:             id,
		OauthExpiresAt: pgtype.Timestamptz{Time: expiresBefore, Valid: true},
	})
}

// UpdateOAuthTokens atomically rewrites id's own value_encrypted/
// oauth_expires_at together -- the refresh pump's own success path
// (§29.5), never one without the other.
func (s *ProviderCredentialStore) UpdateOAuthTokens(ctx context.Context, id pgtype.UUID, valueEncrypted []byte, oauthExpiresAt time.Time) (sqlcgen.ProviderCredential, error) {
	return s.q.UpdateOAuthProviderCredentialTokens(ctx, sqlcgen.UpdateOAuthProviderCredentialTokensParams{
		ID:             id,
		ValueEncrypted: valueEncrypted,
		OauthExpiresAt: pgtype.Timestamptz{Time: oauthExpiresAt, Valid: true},
	})
}

// MarkNeedsRelink sets id's own oauth_needs_relink true -- the refresh
// pump's own terminal-failure path (§29.5: invalid_grant/
// refresh_token_reused).
func (s *ProviderCredentialStore) MarkNeedsRelink(ctx context.Context, id pgtype.UUID) (sqlcgen.ProviderCredential, error) {
	return s.q.MarkProviderCredentialNeedsRelink(ctx, id)
}

// WithTx returns a ProviderCredentialStore whose queries run on tx instead
// of the pool this store was built with -- mirrors IdentityLinkPromptStore
// .WithTx exactly; used by internal/app/chatgptlink's own link/relink
// transaction (which also writes/deletes a chatgpt_link_attempts row in
// the SAME transaction) and by the refresh pump's own per-batch
// transaction (internal/app/chatgptrefresh, §29.5).
func (s *ProviderCredentialStore) WithTx(tx pgx.Tx) *ProviderCredentialStore {
	return &ProviderCredentialStore{q: s.q.WithTx(tx)}
}
