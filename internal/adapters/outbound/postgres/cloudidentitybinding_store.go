package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// CloudIdentityBindingStore is a thin, pass-through wrapper around the
// sqlc-generated cloud_identity_bindings queries (Step 73a, "cloud
// identity: OIDC issuer, bindings, minting", §27.3,
// migrations/000093_cloud_identity_bindings.up.sql). No caching, no
// retries, no business rules -- kind/scope validation (including the
// azure+global refusal, this Step's own gap-3 resolution) lives in
// internal/domain/cloudidentity.ValidateBinding, called by the httpapi
// layer before this store is ever reached, mirroring every other store's
// "pure pass-through, validation lives one layer up" convention in this
// package.
type CloudIdentityBindingStore struct {
	q *sqlcgen.Queries
}

// NewCloudIdentityBindingStore builds a CloudIdentityBindingStore backed
// by pool.
func NewCloudIdentityBindingStore(pool *pgxpool.Pool) *CloudIdentityBindingStore {
	return &CloudIdentityBindingStore{q: sqlcgen.New(pool)}
}

// WithTx returns a CloudIdentityBindingStore whose queries run on tx
// instead of the pool this store was built with -- the httpapi layer uses
// this to write a binding's own audit_log row in the SAME transaction as
// the change itself (§13.3: "written in the same transaction as the
// change"), mirroring every other store's WithTx convention in this
// package.
func (s *CloudIdentityBindingStore) WithTx(tx pgx.Tx) *CloudIdentityBindingStore {
	return &CloudIdentityBindingStore{q: s.q.WithTx(tx)}
}

// Create inserts a new cloud_identity_bindings row. scopeTargetID is nil
// for scope=global (enforced by the table's own CHECK constraint on a
// mismatch); a duplicate (scope, scopeTargetID, kind) fails against one
// of the table's own two partial unique indexes, and kind=azure at
// scope=global fails cloud_identity_bindings_no_azure_global -- the
// caller maps either into a 4xx, never retried here.
func (s *CloudIdentityBindingStore) Create(ctx context.Context, scope sqlcgen.CloudIdentityBindingScope, scopeTargetID *string, kind sqlcgen.CloudIdentityBindingKind, audience string, params []byte) (sqlcgen.CloudIdentityBinding, error) {
	return s.q.CreateCloudIdentityBinding(ctx, sqlcgen.CreateCloudIdentityBindingParams{
		Scope:         scope,
		ScopeTargetID: scopeTargetID,
		Kind:          kind,
		Audience:      audience,
		Params:        params,
	})
}

// Get fetches one row by id. pgx.ErrNoRows (unwrapped) means no such row.
func (s *CloudIdentityBindingStore) Get(ctx context.Context, id pgtype.UUID) (sqlcgen.CloudIdentityBinding, error) {
	return s.q.GetCloudIdentityBinding(ctx, id)
}

// ListByScope lists every row at exactly one (scope, scopeTargetID) pair
// -- scopeTargetID is nil for scope=global, matching Create's own
// nil-means-global convention.
func (s *CloudIdentityBindingStore) ListByScope(ctx context.Context, scope sqlcgen.CloudIdentityBindingScope, scopeTargetID *string) ([]sqlcgen.CloudIdentityBinding, error) {
	return s.q.ListCloudIdentityBindingsByScope(ctx, sqlcgen.ListCloudIdentityBindingsByScopeParams{
		Scope:         scope,
		ScopeTargetID: scopeTargetID,
	})
}

// Update rotates id's own audience/params in place -- scope/
// scopeTargetID/kind are immutable once created (see this package's own
// queries/cloudidentitybindings.sql doc comment for why).
func (s *CloudIdentityBindingStore) Update(ctx context.Context, id pgtype.UUID, audience string, params []byte) (sqlcgen.CloudIdentityBinding, error) {
	return s.q.UpdateCloudIdentityBinding(ctx, sqlcgen.UpdateCloudIdentityBindingParams{
		ID:       id,
		Audience: audience,
		Params:   params,
	})
}

// Delete removes id's own row, returning the number of rows actually
// deleted (0 or 1) -- the caller renders 404 on 0, matching this
// package's own established "affected-row-count decides the caller's own
// not-found branch" convention.
func (s *CloudIdentityBindingStore) Delete(ctx context.Context, id pgtype.UUID) (int64, error) {
	return s.q.DeleteCloudIdentityBinding(ctx, id)
}

// ListForResolution fetches every candidate binding (global, plus this
// session's own environment if it has one) whose audience matches
// requestedAudience -- the minting endpoint's own single read.
// environmentID nil means "this session has no attached Environment"
// (matches nothing at that scope, never a wildcard).
func (s *CloudIdentityBindingStore) ListForResolution(ctx context.Context, requestedAudience string, environmentID *string) ([]sqlcgen.CloudIdentityBinding, error) {
	return s.q.ListCloudIdentityBindingsForResolution(ctx, sqlcgen.ListCloudIdentityBindingsForResolutionParams{
		Audience:      requestedAudience,
		EnvironmentID: environmentID,
	})
}

// ListForSession fetches EVERY candidate binding (global, plus this
// session's own environment if it has one) regardless of audience --
// Step 73b's own sandbox-facing cloud-identity-config delivery endpoint's
// single read (see queries/cloudidentitybindings.sql's own
// ListCloudIdentityBindingsForSession doc comment for the full "why this
// differs from ListForResolution"). environmentID nil means "this
// session has no attached Environment" (matches nothing at that scope,
// never a wildcard), mirroring ListForResolution's own identical
// convention.
func (s *CloudIdentityBindingStore) ListForSession(ctx context.Context, environmentID *string) ([]sqlcgen.CloudIdentityBinding, error) {
	return s.q.ListCloudIdentityBindingsForSession(ctx, environmentID)
}
