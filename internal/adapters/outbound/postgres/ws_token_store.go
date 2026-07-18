package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// WSTokenStore is a thin, pass-through wrapper around the sqlc-generated
// ws_tokens queries (§4.3, §6.2's ws-token mint/verify mechanism,
// migrations/000016_ws_tokens.up.sql). No caching, no retries, no business
// rules — minting (generating the plaintext token, hashing it, choosing
// its TTL) is internal/adapters/inbound/httpapi's job; verifying a
// presented token against a stored hash is internal/adapters/inbound/
// wshub's job. This store only ever sees already-hashed values.
type WSTokenStore struct {
	q *sqlcgen.Queries
}

// NewWSTokenStore builds a WSTokenStore backed by pool.
func NewWSTokenStore(pool *pgxpool.Pool) *WSTokenStore {
	return &WSTokenStore{q: sqlcgen.New(pool)}
}

// Create inserts a new ws_tokens row and returns it.
func (s *WSTokenStore) Create(ctx context.Context, arg sqlcgen.CreateWSTokenParams) (sqlcgen.WsToken, error) {
	return s.q.CreateWSToken(ctx, arg)
}

// GetByHash fetches a ws_tokens row by its (already-hashed) token_hash —
// the client hub's own verify-by-hash lookup at subscribe time (§6.2).
//
// This is a plain indexed equality lookup, deliberately NOT a fetch-by-
// session-then-crypto/subtle.ConstantTimeCompare (wshub/token.go's own
// sandbox-token precedent) — the two credentials have genuinely different
// shapes: a sandbox has exactly one live token per (session, gen), a
// natural fetch-by-session key, while a session can accumulate many
// ws_tokens rows over time (re-minted, expired), so "hash the presented
// token, look it up by its own indexed hash column" is the correct,
// standard pattern here (the same one GitHub/Stripe-style API-token
// schemes use), not an oversight. A timing side-channel on a B-tree
// equality comparison against a SHA-256 hash of a 256-bit random token,
// over a network+DB round trip, is not a practically exploitable oracle.
func (s *WSTokenStore) GetByHash(ctx context.Context, tokenHash string) (sqlcgen.WsToken, error) {
	return s.q.GetWSTokenByHash(ctx, tokenHash)
}
