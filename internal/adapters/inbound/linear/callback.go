package linear

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/oauth2"

	"github.com/khazaddev/narvi/internal/adapters/outbound/linearapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
)

// NewInstallCallbackHandler backs GET /auth/linear/callback: verifies the
// CSRF-protection state cookie NewInstallHandler set, exchanges the
// authorization code for Linear's own workspace-scoped OAuth token pair,
// fetches the workspace's own app-user id + organization id (so the
// stored row can be keyed by the SAME organization id every later
// AgentSessionEvent webhook carries), encrypts both tokens at rest, and
// upserts one linear_installations row.
//
// Structurally mirrors internal/adapters/inbound/auth's own
// NewCallbackHandler (state check -> Exchange -> encrypt -> persist), but
// -- unlike that handler -- there is no allowlist/first-time-vs-returning
// branch here at all: installing (or re-installing) a workspace is
// authorized simply by already being a signed-in Narvi user (see doc.go's
// own scope note on role-gating), and Upsert always just replaces the
// prior token pair in place (see migrations/000029_linear_installations.
// up.sql's own doc comment: "there is exactly one live installation per
// organization_id, never a history of past ones").
func NewInstallCallbackHandler(
	oauthConfig *oauth2.Config,
	linearClient *linearapi.Client,
	installations *postgres.LinearInstallationStore,
	tokenEncryptionKey []byte,
	secureCookies bool,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		stateCookie, cookieErr := r.Cookie(installStateCookieName)
		queryState := r.URL.Query().Get("state")
		if cookieErr != nil || stateCookie.Value == "" || stateCookie.Value != queryState {
			logger.Warn("linear: install callback rejected: state mismatch")
			http.Error(w, "invalid or missing oauth state", http.StatusBadRequest)
			return
		}
		http.SetCookie(w, expiredCookie(installStateCookieName, secureCookies))

		code := r.URL.Query().Get("code")
		token, err := oauthConfig.Exchange(ctx, code)
		if err != nil {
			// Mirrors internal/adapters/inbound/auth's own NewCallbackHandler
			// treatment of an exchange failure exactly (see that handler's
			// own doc comment for the full reasoning): always 401, never
			// attempting to distinguish a bad/reused code from a genuine
			// backend/network problem.
			logger.Warn("linear: install callback rejected: oauth exchange failed", "error", err)
			http.Error(w, "oauth exchange failed", http.StatusUnauthorized)
			return
		}

		appUserID, organizationID, err := linearClient.ViewerAndOrganization(ctx, token.AccessToken)
		if err != nil {
			logger.Error("linear: fetch viewer/organization failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		encryptedAccessToken, err := platform.EncryptToken(tokenEncryptionKey, []byte(token.AccessToken))
		if err != nil {
			logger.Error("linear: encrypt access token failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		var encryptedRefreshToken []byte
		if token.RefreshToken != "" {
			encryptedRefreshToken, err = platform.EncryptToken(tokenEncryptionKey, []byte(token.RefreshToken))
			if err != nil {
				logger.Error("linear: encrypt refresh token failed", "error", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
		}

		var connectedBy pgtype.UUID
		if authUser, ok := platform.UserFromContext(ctx); ok {
			// Best-effort attribution only (see this handler's own doc
			// comment) -- a parse failure here must never block an
			// otherwise-successful installation, so it is logged and
			// left as a genuine NULL rather than surfaced as an error.
			if err := connectedBy.Scan(authUser.ID); err != nil {
				logger.Warn("linear: parse authenticated user id failed", "error", err)
				connectedBy = pgtype.UUID{}
			}
		}

		var expiresAt pgtype.Timestamptz
		if err := expiresAt.Scan(token.Expiry); err != nil {
			logger.Error("linear: scan token expiry failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		if _, err := installations.Upsert(ctx, sqlcgen.UpsertLinearInstallationParams{
			OrganizationID:        organizationID,
			AppUserID:             appUserID,
			AccessTokenEncrypted:  encryptedAccessToken,
			RefreshTokenEncrypted: encryptedRefreshToken,
			ExpiresAt:             expiresAt,
			ConnectedByUserID:     connectedBy,
		}); err != nil {
			logger.Error("linear: upsert installation failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		logger.Info("linear: workspace installed", "organization_id", organizationID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"connected"}`))
	}
}
