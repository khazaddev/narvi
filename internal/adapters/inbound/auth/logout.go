package auth

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/platform"
)

// NewLogoutHandler backs POST /auth/logout (§13.1): reads the auth-session
// cookie; if present, hashes it and DELETES the matching user_sessions row
// -- a REAL DB delete, genuine revocation (§13.1's own "backend-issued"
// wording is exactly why this must be possible: unlike a stateless signed
// cookie, this session can actually be revoked server-side, unlike merely
// asking the browser to forget it). A missing cookie, or a cookie whose
// hash matches no row (already gone), is NOT an error -- logout is
// idempotent, calling it twice in a row never fails. Either way, the
// response always clears the browser's own cookie via
// platform.ExpiredAuthSessionCookie.
func NewLogoutHandler(userSessions *postgres.UserSessionStore, secureCookies bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		if cookie, err := r.Cookie(platform.AuthSessionCookieName); err == nil && cookie.Value != "" {
			row, lookupErr := userSessions.GetByHash(ctx, platform.HashToken(cookie.Value))
			switch {
			case lookupErr == nil:
				if delErr := userSessions.Delete(ctx, row.ID); delErr != nil {
					logger.Error("auth: logout: delete user session failed", "error", delErr)
				}
			case errors.Is(lookupErr, pgx.ErrNoRows):
				// Already gone -- logout is idempotent, this is fine.
			default:
				logger.Error("auth: logout: lookup user session failed", "error", lookupErr)
			}
		}

		http.SetCookie(w, platform.ExpiredAuthSessionCookie(secureCookies))
		w.WriteHeader(http.StatusNoContent)
	}
}
