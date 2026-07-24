package auth

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/platform"
)

// Middleware gates a chi route group behind a valid, backend-issued
// user-session cookie (§13.4: "route middleware"). This remains a "must be
// logged in" gate ONLY -- it still does not itself add any role-based
// gating at the ROUTE level (that stays a coarse "authenticated or not"
// check, exactly per §13.3's own "HTTP middleware handles the coarse
// route-level gate"). platform.AuthenticatedUser.Role IS now real,
// load-bearing data, though: Step 39 ("identities + full RBAC") reads it
// straight out of context (platform.UserFromContext) in every
// state-changing REST handler downstream (internal/adapters/inbound/
// httpapi) to build a domain/authz.Actor and render the real §13.3
// verdict per request -- this middleware's own job is unchanged, only
// what a caller further down the chain does with the Role it already
// carried is new.
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

			authUser, ok := Authenticate(ctx, userSessions, users, r)
			if !ok {
				writeUnauthorized(w)
				return
			}

			next.ServeHTTP(w, r.WithContext(platform.WithUser(ctx, authUser)))
		})
	}
}

// Authenticate resolves r's own narvi_auth_session cookie into a real,
// non-disabled platform.AuthenticatedUser -- the exact same 4-step check
// Middleware above already performed inline (cookie present -> hash found
// -> not expired -> user not disabled), extracted here (Step 39,
// "identities + full RBAC", §13.2) so a SECOND caller can reuse the
// identical check without going through chi middleware at all: internal/
// adapters/inbound/identitylink's magic-link consume handler needs to
// know "is this visitor signed in" and, if not, REDIRECT them into the
// login flow (never a bare 401 JSON body the way Middleware's own
// rejection responds) -- a fundamentally different response shape than
// Middleware's own gate, but the exact same underlying authentication
// check either way. ok=false means any of the 4 checks failed (see this
// package's own security note above on why none of them are
// distinguished in a caller-visible way); every rejection reason is still
// logged server-side, identically to Middleware's own previous inline
// logging.
func Authenticate(ctx context.Context, userSessions *postgres.UserSessionStore, users *postgres.UserStore, r *http.Request) (platform.AuthenticatedUser, bool) {
	logger := platform.Logger(ctx)

	cookie, err := r.Cookie(platform.AuthSessionCookieName)
	if err != nil || cookie.Value == "" {
		logger.Warn("auth: authenticate rejected: no session cookie")
		return platform.AuthenticatedUser{}, false
	}

	row, err := userSessions.GetByHash(ctx, platform.HashToken(cookie.Value))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			logger.Warn("auth: authenticate rejected: session hash not found")
		} else {
			logger.Error("auth: authenticate: lookup session failed", "error", err)
		}
		return platform.AuthenticatedUser{}, false
	}

	if row.ExpiresAt.Time.Before(time.Now()) {
		logger.Warn("auth: authenticate rejected: session expired")
		return platform.AuthenticatedUser{}, false
	}

	userRow, err := users.GetByID(ctx, row.UserID)
	if err != nil {
		logger.Error("auth: authenticate: lookup user failed", "error", err)
		return platform.AuthenticatedUser{}, false
	}
	if userRow.Disabled {
		logger.Warn("auth: authenticate rejected: user disabled")
		return platform.AuthenticatedUser{}, false
	}

	return platform.AuthenticatedUser{
		ID:    userRow.ID.String(),
		Role:  string(userRow.Role),
		Email: userRow.PrimaryEmail,
	}, true
}

// writeUnauthorized writes the single, generic 401 body every rejection
// path in Middleware shares -- see this file's own top comment on why no
// rejection reason is ever distinguished in the response.
func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
}
