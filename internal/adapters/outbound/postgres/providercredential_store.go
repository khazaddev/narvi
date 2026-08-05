package postgres

import (
	"context"

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

// ListForResolution fetches every candidate row (across all 3 scopes, for
// every provider at once) that could apply to a session naming
// repoFullNames and, optionally, environmentID -- the sandbox-facing
// delivery endpoint's own single read. repoFullNames may be empty (never
// an error); environmentID nil means "this session has no attached
// Environment" (matches nothing at the environment scope, never a wildcard).
func (s *ProviderCredentialStore) ListForResolution(ctx context.Context, repoFullNames []string, environmentID *string) ([]sqlcgen.ProviderCredential, error) {
	if repoFullNames == nil {
		repoFullNames = []string{}
	}
	return s.q.ListProviderCredentialsForResolution(ctx, sqlcgen.ListProviderCredentialsForResolutionParams{
		RepoFullNames: repoFullNames,
		EnvironmentID: environmentID,
	})
}
