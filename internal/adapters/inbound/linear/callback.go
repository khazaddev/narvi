package linear

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"

	"github.com/narvidev/narvi/internal/adapters/outbound/linearapi"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/app/auditlog"
	"github.com/narvidev/narvi/internal/platform"
)

// NewInstallCallbackHandler backs GET /auth/linear/callback: verifies the
// CSRF-protection state cookie NewInstallHandler set, exchanges the
// authorization code for Linear's own workspace-scoped OAuth token pair,
// fetches the workspace's own app-user id + organization id (so the
// stored row can be keyed by the SAME organization id every later
// AgentSessionEvent webhook carries), encrypts both tokens at rest, and
// upserts one linear_installations row -- in the SAME transaction as an
// audit_log entry (authz.go's own requireManageIntegrations gate below,
// and this handler's own transactional Upsert+Record, close the two gaps
// a confirmed audit finding raised: no role check at all ahead of a
// token-replacing write, and that write leaving no audit_log trail,
// unlike every other admin-tier mutation in this codebase).
//
// Structurally mirrors internal/adapters/inbound/auth's own
// NewCallbackHandler (state check -> Exchange -> encrypt -> persist), but
// -- unlike that handler -- there is no allowlist/first-time-vs-returning
// branch here at all: installing (or re-installing) a workspace is
// authorized by requireManageIntegrations (an admin actor, per authz.go's
// own doc comment), and Upsert always just replaces the prior token pair
// in place (see migrations/000031_linear_installations.up.sql's own doc
// comment: "there is exactly one live installation per organization_id,
// never a history of past ones") -- the transaction below looks the row
// up first (before the Upsert) purely to pick the audit action's own
// connected-vs-reconnected label; it never gates behavior.
func NewInstallCallbackHandler(
	oauthConfig *oauth2.Config,
	linearClient *linearapi.Client,
	pool *pgxpool.Pool,
	installations *postgres.LinearInstallationStore,
	auditLog *postgres.AuditLogStore,
	tokenEncryptionKey []byte,
	secureCookies bool,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		actorUserID, ok := requireManageIntegrations(w, r)
		if !ok {
			return
		}

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

		var expiresAt pgtype.Timestamptz
		if err := expiresAt.Scan(token.Expiry); err != nil {
			logger.Error("linear: scan token expiry failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			logger.Error("linear: begin tx failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		// Looked up FIRST, inside the same transaction as the Upsert below,
		// purely to pick the audit action's own connected-vs-reconnected
		// label (see this handler's own doc comment) -- a pgx.ErrNoRows
		// result means no admin has ever connected this workspace before;
		// any OTHER error here is a genuine failure, not "not found".
		action := "integration.linear_connected"
		if _, err := installations.WithTx(tx).GetByOrganizationID(ctx, organizationID); err == nil {
			action = "integration.linear_reconnected"
		} else if !errors.Is(err, pgx.ErrNoRows) {
			logger.Error("linear: look up existing installation failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		if _, err := installations.WithTx(tx).Upsert(ctx, sqlcgen.UpsertLinearInstallationParams{
			OrganizationID:        organizationID,
			AppUserID:             appUserID,
			AccessTokenEncrypted:  encryptedAccessToken,
			RefreshTokenEncrypted: encryptedRefreshToken,
			ExpiresAt:             expiresAt,
			ConnectedByUserID:     actorUserID,
		}); err != nil {
			logger.Error("linear: upsert installation failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		if err := auditlog.Record(ctx, auditLog.WithTx(tx), actorUserID, action, "linear_installation", organizationID, map[string]any{
			"app_user_id": appUserID,
		}); err != nil {
			logger.Error("linear: record audit log failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		if err := tx.Commit(ctx); err != nil {
			logger.Error("linear: commit tx failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		logger.Info("linear: workspace installed", "organization_id", organizationID, "action", action)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"connected"}`))
	}
}
