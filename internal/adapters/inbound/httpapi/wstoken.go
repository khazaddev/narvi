package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
)

// MintWSToken backs POST /api/sessions/{sessionID}/ws-token (§6.2, §6.3):
// 404 if the session doesn't exist; otherwise mints a fresh plaintext
// token (platform.GenerateToken), persists ONLY its hash
// (platform.HashToken) with a platform.Timeouts.WSTokenTTL expiry
// (§6.2: "24h TTL"), and responds 200 with restdtos.WSTokenResponse
// carrying the PLAINTEXT token -- the only time it is ever returned,
// matching §6.2's "hashed at rest" requirement literally. See doc.go's
// own auth-gap writeup for why ws_tokens.user_id stays NULL always in
// this Step (no request body/auth beyond the session existing is
// required or checked).
func MintWSToken(sessions *postgres.SessionStore, wsTokens *postgres.WSTokenStore, timeouts platform.Timeouts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID, ok := parseSessionID(w, r)
		if !ok {
			return
		}
		ctx := platform.WithSessionID(r.Context(), sessionID.String())
		logger := platform.Logger(ctx)

		if _, err := sessions.Get(ctx, sessionID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "session not found")
				return
			}
			logger.Error("httpapi: get session failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		token, err := platform.GenerateToken()
		if err != nil {
			logger.Error("httpapi: generate ws-token failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		expiresAt := time.Now().Add(timeouts.WSTokenTTL)
		if _, err := wsTokens.Create(ctx, sqlcgen.CreateWSTokenParams{
			SessionID: sessionID,
			UserID:    pgtype.UUID{}, // NULL -- see doc.go's own auth-gap writeup.
			TokenHash: platform.HashToken(token),
			ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
		}); err != nil {
			logger.Error("httpapi: create ws-token failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusOK, restdtos.WSTokenResponse{
			Token:     token,
			ExpiresAt: expiresAt,
		})
	}
}
