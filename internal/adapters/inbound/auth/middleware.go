package auth

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/platform"
)

// Middleware gates a chi route group behind a valid, backend-issued
// user-session cookie (§13.4: "route middleware"). This is a "must be
// logged in" gate ONLY -- no route in this Step's own scope needs an
// admin-only check (see doc.go's own scope-boundary section), so this does
// NOT add any role-based gating; platform.AuthenticatedUser.Role is
// carried in context purely for future (Step 39) use.
//
// Every rejection path -- missing cookie, hash-lookup miss, expired row, a
// disabled user's otherwise-valid session -- responds 401 with the SAME
// generic body: an attacker probing for "expired vs. never-existed vs.
// disabled" gets no signal in the RESPONSE either way. Server-side logs
// (via platform.Logger) DO distinguish them, for operators' own
// debugging -- never logging the cookie's own value, only outcome labels.
func Middleware(userSessions *postgres.UserSessionStore, users *postgres.UserStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			logger := platform.Logger(ctx)

			cookie, err := r.Cookie(platform.AuthSessionCookieName)
			if err != nil || cookie.Value == "" {
				logger.Warn("auth: middleware rejected: no session cookie")
				writeUnauthorized(w)
				return
			}

			row, err := userSessions.GetByHash(ctx, platform.HashToken(cookie.Value))
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					logger.Warn("auth: middleware rejected: session hash not found")
				} else {
					logger.Error("auth: middleware: lookup session failed", "error", err)
				}
				writeUnauthorized(w)
				return
			}

			if row.ExpiresAt.Time.Before(time.Now()) {
				logger.Warn("auth: middleware rejected: session expired")
				writeUnauthorized(w)
				return
			}

			userRow, err := users.GetByID(ctx, row.UserID)
			if err != nil {
				logger.Error("auth: middleware: lookup user failed", "error", err)
				writeUnauthorized(w)
				return
			}
			if userRow.Disabled {
				logger.Warn("auth: middleware rejected: user disabled")
				writeUnauthorized(w)
				return
			}

			authUser := platform.AuthenticatedUser{
				ID:    userRow.ID.String(),
				Role:  string(userRow.Role),
				Email: userRow.PrimaryEmail,
			}
			next.ServeHTTP(w, r.WithContext(platform.WithUser(ctx, authUser)))
		})
	}
}

// writeUnauthorized writes the single, generic 401 body every rejection
// path in Middleware shares -- see this file's own top comment on why no
// rejection reason is ever distinguished in the response.
func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
}
