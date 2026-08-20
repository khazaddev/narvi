// This file (snapshotmint.go) implements POST /sessions/{sessionID}/
// snapshot (§3.2, "snapshots & restore", design decision 2) -- the
// control-plane side of the round trip design decision 2 reasons through
// in full: TakeSnapshot's real network call can only be made by the
// control plane (it needs the provider's own credentials/base URL, which
// the sandbox-agent process has neither access to nor any business
// having), while the ack'd "snapshot_ready" wire event is the sandbox's
// own authoritative report of the real snapshotId this endpoint mints.
//
// Deliberately mounted OUTSIDE auth.Middleware (§13.1's cookie-based,
// browser-user auth) and outside /api/sessions -- mirrors
// scmcredentials.go's own precedent exactly (see that file's own doc
// comment): this is a SANDBOX-bearer-token-authenticated route, not a
// browser-facing one, matching internal/adapters/inbound/wshub/sandbox.go's
// own header-bearer-token handshake precedent from §3.2. See
// cmd/control-plane/main.go's own mounting.
//
// Audit remediation (security-crosscutting lens): this endpoint originally
// checked only the sandbox bearer token and provider_id, unlike its own
// sibling scm-credentials.go -- which the SAME audit pass already fixed to
// enforce dead-sandbox and gen parity. Because a sandbox's own token_hash
// is neither cleared nor rotated when it terminalizes (only ever
// overwritten by a later respawn's own UpsertSandboxForSpawn), a
// terminalized-but-still-live sandbox's last bearer token stayed valid
// against THIS endpoint indefinitely -- forcing an unbounded number of real
// provider TakeSnapshot calls. Fixed below by mirroring scmcredentials.go's
// own two guards, in the SAME order, at the SAME point (immediately after
// the sandbox row lookup, before the token comparison): IsDeadSandboxStatus
// -> 410, then an X-Sandbox-Gen header check against sandboxRow.Gen -> 403.
// See ScmCredentials' own doc comment for the full reasoning behind that
// ordering and behind checking gen at all despite token_hash's own
// overwrite-on-respawn behavior already closing most of the same window.
//
// The sandbox-agent side of this round trip (internal/sandboxagent/
// snapshotclient.Client.Mint) was updated in the SAME change to always send
// X-Sandbox-Gen -- this endpoint's own new gen check would otherwise reject
// every real production snapshot-mint request, not just a stale/replayed
// one.

package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/domain/sandbox"
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
//  2. Authorization: Bearer <token> missing/malformed -> 401 (checked
//     structurally before the sandbox row lookup below -- a malformed
//     header is rejected without a Postgres round trip at all).
//  3. sandbox.IsDeadSandboxStatus(sandboxRow.Status) -> 410 (mirrors
//     scmcredentials.go's own "session stopped" status/message
//     convention). Checked immediately after the sandbox row lookup,
//     BEFORE the gen check and the real token comparison below -- same
//     ordering scmcredentials.go itself uses, since a terminalized
//     sandbox's last-known-live token_hash/gen are no longer meaningful
//     to compare against at all (audit remediation; see this file's own
//     doc comment above).
//  4. The presented X-Sandbox-Gen header is missing/malformed, or parses
//     but does not equal sandboxRow.Gen -> 403 (mirrors scmcredentials.
//     go's own gen-mismatch reasoning, §9.3 scenario #6; audit
//     remediation, see this file's own doc comment above).
//  5. The presented bearer token fails verifySandboxBearerToken -> 401.
//  6. The sandbox row has no live provider_id (nil/empty -- e.g. a
//     sandbox that was never actually spawned against a real provider
//     object, or one whose provider_id was cleared by some future path)
//     -> 409 Conflict. Chosen over 500: this is an honest statement about
//     the CURRENT STATE of the resource this request names ("there is no
//     live provider sandbox to snapshot right now"), not a server
//     malfunction -- the same distinction a plain REST client would
//     expect 409 to carry elsewhere in this API family.
//  7. The real provider.TakeSnapshot call returns an error (a
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
//  8. Otherwise -> 200 with snapshotMintResponse{SnapshotID: <the real
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

		// Dead-sandbox check FIRST, before the gen/token comparisons below --
		// same ordering scmcredentials.go's own ScmCredentials handler uses
		// (see this func's own doc comment step 3, and this file's own doc
		// comment above, for why). A Suspect sandbox is deliberately NOT
		// dead (IsDeadSandboxStatus is a deny-list, not an allow-list) -- it
		// must still be able to mint a snapshot.
		if sandbox.IsDeadSandboxStatus(sandbox.State(sandboxRow.Status)) {
			writeError(w, http.StatusGone, "session stopped")
			return
		}

		// Gen check (this func's own doc comment step 4): defense-in-depth
		// alongside the token comparison below, mirroring scmcredentials.
		// go's own identical reasoning -- see that file's own doc comment.
		// Missing/malformed gen is treated identically to a mismatch (403):
		// a well-formed caller (snapshotclient.Client.Mint, updated by this
		// same audit remediation to always send it) always presents one, so
		// there is no legitimate case to distinguish here.
		presentedGen, genErr := strconv.Atoi(r.Header.Get("X-Sandbox-Gen"))
		if genErr != nil || presentedGen != int(sandboxRow.Gen) {
			logger.Warn("httpapi: snapshot-mint: rejecting: gen mismatch",
				"presented_gen_header", r.Header.Get("X-Sandbox-Gen"), "sandbox_gen", sandboxRow.Gen)
			writeError(w, http.StatusForbidden, "no usable snapshot credential for this session")
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
