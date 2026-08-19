package oidckey

import "time"

// SigningKey is one oidc_signing_keys row's minimal shape this package's
// functions need -- the caller (internal/adapters/outbound/postgres.
// OIDCSigningKeyStore) converts from sqlcgen.OidcSigningKey; this package
// itself never touches Postgres or pgtype (§11).
type SigningKey struct {
	Kid       string
	CreatedAt time.Time
	// RetiredAt is nil for the single currently-active (signing) key, or
	// the instant RotateSigningKeys retired it otherwise.
	RetiredAt *time.Time
}

// IsActive reports whether k is the currently-active signing key -- the
// one a brand-new token should be signed with. At most one key in any
// given already-fetched set should ever report true (enforced by
// oidc_signing_keys_one_active_uniq at the storage layer, migrations/
// 000092_oidc_signing_keys.up.sql); this function itself makes no such
// uniqueness claim about its caller's input, it only answers the
// question for one key at a time.
func IsActive(k SigningKey) bool {
	return k.RetiredAt == nil
}

// IsPublishable reports whether k should still appear in the JWKS
// response (and therefore still be trusted to verify a token signed
// under it) at instant now, given overlap -- the currently-active key
// always is; a retired key is until overlap has elapsed since its own
// RetiredAt (see this package's own doc comment for the full "why").
func IsPublishable(k SigningKey, now time.Time, overlap time.Duration) bool {
	if k.RetiredAt == nil {
		return true
	}
	return now.Before(k.RetiredAt.Add(overlap))
}

// FilterPublishable returns the subset of keys that IsPublishable(k, now,
// overlap) accepts, preserving input order -- the JWKS endpoint's own
// pure filtering step, run over whatever internal/adapters/outbound/
// postgres.OIDCSigningKeyStore.ListPublishable already fetched (that
// store method applies the identical cutoff at the SQL layer as a
// performance optimization -- fetching every retired-forever key on every
// JWKS request would be wasteful -- so in practice this function is a
// second, redundant-but-cheap confirmation over an already-filtered set,
// not the only place the rule is enforced; kept here anyway so the rule
// itself has exactly one, testable, pure home rather than living only as
// a SQL WHERE clause with no equivalent Go-level assertion).
func FilterPublishable(keys []SigningKey, now time.Time, overlap time.Duration) []SigningKey {
	out := make([]SigningKey, 0, len(keys))
	for _, k := range keys {
		if IsPublishable(k, now, overlap) {
			out = append(out, k)
		}
	}
	return out
}
