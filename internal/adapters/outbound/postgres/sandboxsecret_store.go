package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// SandboxSecretStore is a thin, pass-through wrapper around the
// sqlc-generated sandbox_secrets queries ("sandbox secrets &
// opencode config", §27.1, migrations/000090_sandbox_secrets.up.sql) --
// this codebase's SECOND generic secret-storage table, mirroring
// ProviderCredentialStore's own shape exactly. No caching, no retries, no
// business rules, and (like every store in this package) NO encryption/
// decryption of its own: ValueEncrypted always travels through this store
// as opaque ciphertext bytes -- internal/adapters/inbound/httpapi is the
// ONLY layer that ever calls platform.EncryptToken/DecryptToken over it.
type SandboxSecretStore struct {
	q *sqlcgen.Queries
}

// NewSandboxSecretStore builds a SandboxSecretStore backed by pool.
func NewSandboxSecretStore(pool *pgxpool.Pool) *SandboxSecretStore {
	return &SandboxSecretStore{q: sqlcgen.New(pool)}
}

// WithTx returns a SandboxSecretStore whose queries run on tx instead of
// the pool this store was built with -- mirrors every other store's own
// identical WithTx convention (EnvironmentStore, RepoSettingsStore,
// AutomationStore, UserStore, IdentityStore, AuditLogStore). No existing
// caller needed this before Step 75 ("config/data seeding", §13.4):
// internal/adapters/inbound/httpapi/sandboxsecrets.go's own
// createSandboxSecret writes a row with no accompanying audit-log entry
// in the same transaction (see that file's own top doc comment -- it has
// none at all today), so it never needed a shared-transaction handle.
// internal/app/seed does: §13.3 requires "audit_log ... written in the
// same transaction as the change", and this store's own Create is that
// change, so it needs the same WithTx seam every other store already
// has.
func (s *SandboxSecretStore) WithTx(tx pgx.Tx) *SandboxSecretStore {
	return &SandboxSecretStore{q: s.q.WithTx(tx)}
}

// Create inserts a new sandbox_secrets row. scopeTargetID is nil for
// scope=global (enforced by the table's own CHECK constraint on a
// mismatch); a duplicate (scope, scopeTargetID, name) fails against one
// of the table's own two partial unique indexes -- the caller maps that
// into a 409, never retried here.
func (s *SandboxSecretStore) Create(ctx context.Context, scope sqlcgen.SandboxSecretScope, scopeTargetID *string, name string, valueEncrypted []byte) (sqlcgen.SandboxSecret, error) {
	return s.q.CreateSandboxSecret(ctx, sqlcgen.CreateSandboxSecretParams{
		Scope:          scope,
		ScopeTargetID:  scopeTargetID,
		Name:           name,
		ValueEncrypted: valueEncrypted,
	})
}

// Get fetches one row by id. pgx.ErrNoRows (unwrapped) means no such row.
func (s *SandboxSecretStore) Get(ctx context.Context, id pgtype.UUID) (sqlcgen.SandboxSecret, error) {
	return s.q.GetSandboxSecret(ctx, id)
}

// ListByScope lists every row at exactly one (scope, scopeTargetID) pair
// -- scopeTargetID is nil for scope=global, matching Create's own
// nil-means-global convention.
func (s *SandboxSecretStore) ListByScope(ctx context.Context, scope sqlcgen.SandboxSecretScope, scopeTargetID *string) ([]sqlcgen.SandboxSecret, error) {
	return s.q.ListSandboxSecretsByScope(ctx, sqlcgen.ListSandboxSecretsByScopeParams{
		Scope:         scope,
		ScopeTargetID: scopeTargetID,
	})
}

// UpdateValue rotates id's own encrypted value in place -- scope/
// scopeTargetID/name are immutable once created (see
// httpapi/sandboxsecrets.go's own doc comment for why).
func (s *SandboxSecretStore) UpdateValue(ctx context.Context, id pgtype.UUID, valueEncrypted []byte) (sqlcgen.SandboxSecret, error) {
	return s.q.UpdateSandboxSecretValue(ctx, sqlcgen.UpdateSandboxSecretValueParams{
		ID:             id,
		ValueEncrypted: valueEncrypted,
	})
}

// Delete removes id's own row, returning the number of rows actually
// deleted (0 or 1) -- the caller renders 404 on 0, matching this
// package's own established "affected-row-count decides the caller's own
// not-found branch" convention.
func (s *SandboxSecretStore) Delete(ctx context.Context, id pgtype.UUID) (int64, error) {
	return s.q.DeleteSandboxSecret(ctx, id)
}

// ListForResolution fetches every candidate row (across every scope this
// Step actually resolves, for every name at once) that could apply to a
// session naming repoFullNames and optionally environmentID -- the
// sandbox-facing delivery endpoint's own single read. repoFullNames may
// be empty (never an error); environmentID nil means "this session has no
// attached Environment" (matches nothing at that scope, never a
// wildcard). Deliberately no automationID parameter -- §27.1's own
// schema-only carve-out for ScopeAutomation, see
// ListSandboxSecretsForResolution's own doc comment.
func (s *SandboxSecretStore) ListForResolution(ctx context.Context, repoFullNames []string, environmentID *string) ([]sqlcgen.SandboxSecret, error) {
	if repoFullNames == nil {
		repoFullNames = []string{}
	}
	return s.q.ListSandboxSecretsForResolution(ctx, sqlcgen.ListSandboxSecretsForResolutionParams{
		RepoFullNames: repoFullNames,
		EnvironmentID: environmentID,
	})
}
