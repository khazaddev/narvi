// This file (snapshotmint.go) implements POST /sessions/{sessionID}/
// snapshot (Step 22, "snapshots & restore", design decision 2) -- the
// control-plane side of the round trip design decision 2 reasons through
// in full: TakeSnapshot's real network call can only be made by the
// control plane (it needs the provider's own credentials/base URL, which
// the sandbox-agent process has neither access to nor any business
// having), while the ack'd "snapshot_ready" wire event is the sandbox's
// own authoritative report of the real snapshotId this endpoint mints.
//
// Deliberately mounted OUTSIDE auth.Middleware (Step 20's cookie-based,
// browser-user auth) and outside /api/sessions -- mirrors
// scmcredentials.go's own precedent exactly (see that file's own doc
// comment): this is a SANDBOX-bearer-token-authenticated route, not a
// browser-facing one, matching internal/adapters/inbound/wshub/sandbox.go's
// own header-bearer-token handshake precedent from Step 18. See
// cmd/control-plane/main.go's own mounting.

package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// snapshotMintResponse is the wire shape internal/sandboxagent/
// snapshotclient's own Client.Mint expects back on a 2xx -- mirrors that
// package's own request/response shape exactly, deliberately, not
// coincidentally (same reconciliation discipline scmCredentialsResponse
// already established for scmcredentials.go/cpclient.go).
type snapshotMintResponse struct {
	SnapshotID string `json:"snapshotId"`
}

// SnapshotMint backs POST /sessions/{sessionID}/snapshot (note: no /api
// prefix -- a sandbox-to-CP endpoint, not a browser-facing REST route,
// mirroring scm-credentials exactly). Outcome table (design decision 2):
//
//  1. sessionID does not parse as a UUID, or no sandbox row exists for it
//     -> 404 (mirrors scmcredentials.go's own "malformed and nonexistent
//     both mean no such session" precedent -- this caller is
//     sandbox-agent code, never a browser).
//  2. Authorization: Bearer <token> missing/malformed, or the presented
//     token fails verifySandboxBearerToken -> 401.
//  3. The sandbox row has no live provider_id (nil/empty -- e.g. a
//     sandbox that was never actually spawned against a real provider
//     object, or one whose provider_id was cleared by some future path)
//     -> 409 Conflict. Chosen over 500: this is an honest statement about
//     the CURRENT STATE of the resource this request names ("there is no
//     live provider sandbox to snapshot right now"), not a server
//     malfunction -- the same distinction a plain REST client would
//     expect 409 to carry elsewhere in this API family.
//  4. The real provider.TakeSnapshot call returns an error (a
//     *ports.ProviderError, transient or permanent, or any other error)
//     -> 502 Bad Gateway. Chosen over 500: the control plane itself did
//     nothing wrong here -- an upstream dependency (the sandbox provider)
//     failed -- and 502 is the conventional way to say "the thing I asked
//     a downstream service to do, it couldn't do" without conflating that
//     with a bug in this handler itself. Deliberately NOT fed into
//     app/sessionactor's own spawn circuit breaker (spawn_failure_count/
//     last_spawn_failure_at) -- see this Step's own design decision 2:
//     "a snapshot failure is NOT a spawn failure," a distinct concern this
//     handler has no access to that circuit breaker's own write path
//     anyway (it lives in app/sessionactor, not here).
//  5. Otherwise -> 200 with snapshotMintResponse{SnapshotID: <the real
//     id TakeSnapshot returned>}.
func SnapshotMint(
	sandboxes *postgres.SandboxStore,
	provider ports.SandboxProvider,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Mirrors scmcredentials.go's own ScmCredentials handler exactly:
		// a malformed session id is 404, not 400 -- this caller is
		// sandbox-agent code, never a browser, so there is no "malformed
		// vs not-found" REST distinction worth preserving here (see
		// helpers.go's own parseSessionID doc comment for why THAT
		// distinction exists for the browser-facing routes instead).
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
			logger.Error("httpapi: snapshot-mint: get sandbox failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if !verifySandboxBearerToken(token, sandboxRow.TokenHash) {
			writeError(w, http.StatusUnauthorized, "invalid sandbox token")
			return
		}

		if sandboxRow.ProviderID == nil || *sandboxRow.ProviderID == "" {
			writeError(w, http.StatusConflict, "sandbox has no live provider instance to snapshot")
			return
		}

		if provider == nil {
			// Defensive: mirrors app/sessionactor's own nil-provider guards
			// exactly -- some tests, and any deployment genuinely missing
			// one, must not panic here.
			logger.Error("httpapi: snapshot-mint: no SandboxProvider configured")
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		snapshotID, err := provider.TakeSnapshot(ctx, ports.SandboxRef{ProviderID: *sandboxRow.ProviderID})
		if err != nil {
			logger.Error("httpapi: snapshot-mint: TakeSnapshot failed", "error", err)
			writeError(w, http.StatusBadGateway, "snapshot request to provider failed")
			return
		}

		writeJSON(w, http.StatusOK, snapshotMintResponse{SnapshotID: string(snapshotID)})
	}
}
