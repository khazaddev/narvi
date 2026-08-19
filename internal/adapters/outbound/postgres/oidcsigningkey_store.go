package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// OIDCSigningKeyStore is a thin, pass-through wrapper around the
// sqlc-generated oidc_signing_keys queries (Step 73a, "cloud identity:
// OIDC issuer, bindings, minting", §27.3,
// migrations/000092_oidc_signing_keys.up.sql). No caching, no retries, no
// business rules beyond Rotate's own atomicity -- like every other store
// in this package, it never encrypts/decrypts (private_key_encrypted
// always travels as opaque ciphertext) and never generates key material
// itself (internal/adapters/outbound/oidcsigning owns that -- randomness
// belongs in an adapter, never here, and definitely never in
// /internal/domain, §11).
type OIDCSigningKeyStore struct {
	q *sqlcgen.Queries
}

// NewOIDCSigningKeyStore builds an OIDCSigningKeyStore backed by pool.
func NewOIDCSigningKeyStore(pool *pgxpool.Pool) *OIDCSigningKeyStore {
	return &OIDCSigningKeyStore{q: sqlcgen.New(pool)}
}

// WithTx returns an OIDCSigningKeyStore whose queries run on tx instead of
// the pool this store was built with -- Rotate (below) uses this to keep
// its own retire-then-create pair atomic, mirroring every other store's
// WithTx convention in this package.
func (s *OIDCSigningKeyStore) WithTx(tx pgx.Tx) *OIDCSigningKeyStore {
	return &OIDCSigningKeyStore{q: s.q.WithTx(tx)}
}

// GetActive fetches the single currently-active (retired_at IS NULL) key
// -- the one the minting endpoint signs a brand-new token with.
// pgx.ErrNoRows (unwrapped) means no key has ever been provisioned.
func (s *OIDCSigningKeyStore) GetActive(ctx context.Context) (sqlcgen.OidcSigningKey, error) {
	return s.q.GetActiveOIDCSigningKey(ctx)
}

// ListPublishable fetches every key still inside its own publish window
// as of now: the active key (retired_at IS NULL) plus any key retired
// more recently than overlap ago. The JWKS endpoint's own single read.
func (s *OIDCSigningKeyStore) ListPublishable(ctx context.Context, now time.Time, overlap time.Duration) ([]sqlcgen.OidcSigningKey, error) {
	cutoff := now.Add(-overlap)
	return s.q.ListPublishableOIDCSigningKeys(ctx, pgtype.Timestamptz{Time: cutoff, Valid: true})
}

// Rotate performs Step 73a's own admin-triggered rotation (see
// internal/domain/oidckey's own doc comment for the full "why manual,
// admin-triggered" design decision): inside a single transaction, retires
// whatever key is currently active (a no-op, not an error, when there is
// none yet -- first-ever call bootstraps the first key) as of now, then
// inserts newKid/newPrivateKeyEncrypted/newPublicJWK as the new active
// key. Returns the freshly created row. The caller (httpapi's rotation
// handler) is responsible for beginning/committing the transaction this
// store's WithTx runs on -- Rotate itself does not manage the transaction
// boundary, matching every other multi-statement write in this codebase
// (e.g. httpapi.UpdateMemberRole's own begin/defer-rollback/commit
// shape) rather than hiding it inside the store layer.
func (s *OIDCSigningKeyStore) Rotate(ctx context.Context, now time.Time, newKid string, newPrivateKeyEncrypted, newPublicJWK []byte) (sqlcgen.OidcSigningKey, error) {
	current, err := s.q.GetActiveOIDCSigningKey(ctx)
	switch {
	case err == nil:
		if _, retireErr := s.q.RetireOIDCSigningKey(ctx, sqlcgen.RetireOIDCSigningKeyParams{
			RetiredAt: pgtype.Timestamptz{Time: now, Valid: true},
			Kid:       current.Kid,
		}); retireErr != nil {
			return sqlcgen.OidcSigningKey{}, fmt.Errorf("postgres: retire current oidc signing key: %w", retireErr)
		}
	case errors.Is(err, pgx.ErrNoRows):
		// No active key yet -- first-ever rotation bootstraps the first
		// key, nothing to retire. Not an error.
	default:
		return sqlcgen.OidcSigningKey{}, fmt.Errorf("postgres: get active oidc signing key: %w", err)
	}

	created, err := s.q.CreateOIDCSigningKey(ctx, sqlcgen.CreateOIDCSigningKeyParams{
		Kid:                 newKid,
		PrivateKeyEncrypted: newPrivateKeyEncrypted,
		PublicJwk:           newPublicJWK,
	})
	if err != nil {
		return sqlcgen.OidcSigningKey{}, fmt.Errorf("postgres: create new oidc signing key: %w", err)
	}
	return created, nil
}
