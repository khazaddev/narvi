// This file (scmcredentials.go) implements POST /sessions/{sessionID}/
// scm-credentials (Step 21, "e2e happy path", design decision 8) -- the
// control-plane side of the wire contract internal/sandboxagent/
// credentials.CPClient (Step 15) already built and tested the CLIENT side
// of. See that package's own cpclient.go doc comment: "THE CP ENDPOINT
// THIS TALKS TO DOES NOT EXIST YET... whoever implements Step 21
// reconciles the two sides then." This file is that reconciliation --
// every field name/shape below matches CPClient's own
// scmCredentialsRequest/scmCredentialsResponse exactly, deliberately, not
// coincidentally.
//
// Deliberately mounted OUTSIDE auth.Middleware (Step 20's cookie-based,
// browser-user auth): this is a SANDBOX-bearer-token-authenticated
// endpoint, matching internal/adapters/inbound/wshub/sandbox.go's own
// header-bearer-token handshake precedent from Step 18 exactly, not a
// browser-facing route at all -- see cmd/control-plane/main.go's own
// mounting (outside the /api prefix, alongside the sandbox/client WS
// route, not inside the /api/sessions auth-gated group).

package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/inbound/wshub"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
)

// scmCredentialsRequest mirrors internal/sandboxagent/credentials.
// cpclient.go's own scmCredentialsRequest exactly.
type scmCredentialsRequest struct {
	Host string `json:"host"`
}

// scmCredentialsResponse mirrors internal/sandboxagent/credentials.
// cpclient.go's own scmCredentialsResponse exactly.
type scmCredentialsResponse struct {
	Username  string    `json:"username"`
	Password  string    `json:"password"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// verifySandboxBearerToken duplicates internal/adapters/inbound/wshub's
// own (unexported) verifySandboxToken logic for the HASH COMPARISON half
// only -- SHA-256 hash (wshub.HashSandboxToken, already exported for
// exactly this reuse) + crypto/subtle.ConstantTimeCompare, never a bare
// `==` (Step 18's own established constant-time-comparison discipline).
//
// Deliberately DOES NOT copy wshub's own nil-token_hash bypass (a sandbox
// row with no token_hash yet minted is treated there as "accept any
// non-empty presented token" -- see internal/adapters/inbound/wshub/
// doc.go's own doc comment on verifySandboxToken). That bypass's blast
// radius on the WS handshake is bounded to gating a connection; a
// successful check HERE hands back a real, decrypted GitHub OAuth access
// token -- a genuine live credential, not just a connection -- so this
// endpoint is deliberately stricter: a nil/absent token_hash is an
// immediate reject, never an implicit accept. Confirmed currently
// unreachable in production (every sandbox-row-creation path this Step's
// own code uses -- tryPlanSpawn's own UpsertSandboxForSpawn call,
// internal/app/sessionactor/dispatch.go -- always sets token_hash), but
// the elevated stakes here mean this endpoint must not inherit that
// bypass regardless of whether it is reachable today.
func verifySandboxBearerToken(presented string, storedHash *string) bool {
	if presented == "" || storedHash == nil {
		return false
	}
	got := wshub.HashSandboxToken(presented)
	return subtle.ConstantTimeCompare([]byte(got), []byte(*storedHash)) == 1
}

// ScmCredentials backs POST /sessions/{sessionID}/scm-credentials (note:
// no /api prefix -- a sandbox-to-CP endpoint, not a browser-facing REST
// route, §5.2). Outcome table (design decision 8):
//
//  1. sessionID does not parse as a UUID, or no sandbox row exists for it
//     -> 404 (mirrors wshub/sandbox.go's own "malformed and nonexistent
//     both mean no such session" precedent -- this caller is
//     sandbox-agent code, never a browser).
//  2. Authorization: Bearer <token> missing/malformed, or the presented
//     token fails verifySandboxBearerToken -> 401.
//  3. Malformed request body -> 400.
//  4. The session's own created_by is NULL, OR that user has no linked
//     identities row for provider=github, OR that identity's
//     access_token_encrypted is NULL, OR platform.DecryptToken fails on
//     it -> 403. These four are deliberately grouped as ONE outcome class
//     ("no usable OAuth credential is available for this session's
//     user") -- the honest "no bot/service-account fallback exists"
//     gap named in this Step's own brief, not a bug to work around by
//     inventing a fake bot credential (§8.11's own fallback half is
//     explicitly out of scope). 403 (not 500): this is an
//     authorization-shaped absence from the caller's perspective, not a
//     server malfunction, and mirrors auth.Middleware's own generic-
//     rejection-body discipline (never distinguishing WHICH of the four
//     sub-cases applied, in the response body -- an enumeration-hardening
//     precedent this package already established at Step 20).
//  5. Otherwise -> 200 with scmCredentialsResponse{Username:
//     "x-access-token", Password: <decrypted token>, ExpiresAt: now +
//     timeouts.ScmCredentialTTL}.
//
// The decrypted token is NEVER logged, at any point, under any
// circumstance -- grepped for in this Step's own diff before reporting
// done, exactly like every prior security-sensitive Step's own discipline.
func ScmCredentials(
	sessions *postgres.SessionStore,
	sandboxes *postgres.SandboxStore,
	identities *postgres.IdentityStore,
	tokenEncryptionKey []byte,
	timeouts platform.Timeouts,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var sessionID pgtype.UUID
		if err := sessionID.Scan(chi.URLParam(r, "sessionID")); err != nil {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		ctx = platform.WithSessionID(ctx, sessionID.String())
		logger := platform.Logger(ctx)

		token, ok := bearerTokenFromHeader(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "missing or malformed authorization header")
			return
		}

		sandboxRow, err := sandboxes.Get(ctx, sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "session not found")
				return
			}
			logger.Error("httpapi: scm-credentials: get sandbox failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if !verifySandboxBearerToken(token, sandboxRow.TokenHash) {
			writeError(w, http.StatusUnauthorized, "invalid sandbox token")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		var req scmCredentialsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed request body")
			return
		}

		sessionRow, err := sessions.Get(ctx, sessionID)
		if err != nil {
			logger.Error("httpapi: scm-credentials: get session failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if !sessionRow.CreatedBy.Valid {
			logger.Warn("httpapi: scm-credentials: session has no created_by user; no bot fallback exists (§8.11)")
			writeError(w, http.StatusForbidden, "no usable git credential for this session")
			return
		}

		identity, err := identities.GetByUserAndProvider(ctx, sessionRow.CreatedBy, sqlcgen.IdentityProviderGithub)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				logger.Error("httpapi: scm-credentials: get identity failed", "error", err)
			} else {
				logger.Warn("httpapi: scm-credentials: user has no github identity")
			}
			writeError(w, http.StatusForbidden, "no usable git credential for this session")
			return
		}

		if identity.AccessTokenEncrypted == nil {
			logger.Warn("httpapi: scm-credentials: github identity has no stored access token")
			writeError(w, http.StatusForbidden, "no usable git credential for this session")
			return
		}

		plaintext, err := platform.DecryptToken(tokenEncryptionKey, identity.AccessTokenEncrypted)
		if err != nil {
			// The decrypt error itself is safe to log (it never contains
			// the ciphertext or plaintext -- see platform.DecryptToken's
			// own doc comment), but the plaintext token it would have
			// produced is NEVER logged, here or anywhere else.
			logger.Error("httpapi: scm-credentials: decrypt access token failed", "error", err)
			writeError(w, http.StatusForbidden, "no usable git credential for this session")
			return
		}

		_ = req.Host // the invented contract accepts host but this Step mints one shared credential regardless of it -- see doc comment

		writeJSON(w, http.StatusOK, scmCredentialsResponse{
			Username:  "x-access-token",
			Password:  string(plaintext),
			ExpiresAt: time.Now().Add(timeouts.ScmCredentialTTL),
		})
	}
}

// bearerTokenFromHeader extracts the bearer token from r's Authorization
// header -- mirrors internal/adapters/inbound/wshub/sandbox.go's own
// bearerToken function exactly (that one is unexported to its own
// package; duplicated here rather than exported cross-package for such a
// tiny, dependency-free helper).
func bearerTokenFromHeader(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return "", false
	}
	token := strings.TrimPrefix(h, prefix)
	if token == "" {
		return "", false
	}
	return token, true
}
