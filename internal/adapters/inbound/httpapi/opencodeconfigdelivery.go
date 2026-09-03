// This file (opencodeconfigdelivery.go) implements §27.1's own
// ("sandbox secrets & opencode config", §27.2) CP-side DELIVERY endpoint
// for sandbox-agent: POST /sessions/{sessionID}/opencode-config (note: no
// /api prefix -- a sandbox-to-CP endpoint, not a browser-facing REST
// route, §5.2).
//
// Mirrors sandboxsecretsdelivery.go/providercredentialsdelivery.go's own
// handshake VERBATIM, per §27.2's own "delivered at boot over a sibling
// sandbox-facing endpoint (same handshake)" instruction -- identical
// outcome table (see sandboxsecretsdelivery.go's own top doc comment for
// the numbered list; this endpoint's outcomes 1-6 are byte-for-byte
// identical). Outcome 7 differs in SHAPE, not security posture: this
// endpoint delivers BOTH the global and this session's own environment
// document AT ONCE (§27.2: "delivered at boot... both scopes at once"),
// never narrowed to one winner via providercredential.Resolve -- unlike
// sandbox_secrets/provider_credentials, OpenCode's OWN documented merge
// order (remote -> global -> custom -> project) is what composes the two
// documents together once both are written to disk; this endpoint's own
// job ends at handing sandbox-agent the two raw documents, each present
// or absent independently.

package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/domain/sandbox"
	"github.com/narvidev/narvi/internal/platform"
)

// openCodeConfigDeliveryResponse is this Step's own invented, documented
// response shape: the global document and this session's own environment
// document, each independently either present (raw JSON object bytes) or
// absent (omitted key / JSON null) -- mirrors
// providerCredentialsResponse/sandboxSecretsResponse's own "small,
// explicit, invented wire shape" precedent.
type openCodeConfigDeliveryResponse struct {
	Global      json.RawMessage `json:"global,omitempty"`
	Environment json.RawMessage `json:"environment,omitempty"`
}

// OpenCodeConfigDelivery backs POST /sessions/{sessionID}/opencode-config
// -- see this file's own top doc comment for the full outcome table and
// security-posture rationale.
func OpenCodeConfigDelivery(
	sessions *postgres.SessionStore,
	sandboxes *postgres.SandboxStore,
	openCodeConfigs *postgres.OpenCodeConfigStore,
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
			logger.Error("httpapi: opencode-config: get sandbox failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		// Dead-sandbox check FIRST, before the gen/token comparisons below
		// -- same ordering every other delivery endpoint in this package
		// uses.
		if sandbox.IsDeadSandboxStatus(sandbox.State(sandboxRow.Status)) {
			writeError(w, http.StatusGone, "session stopped")
			return
		}

		presentedGen, genErr := strconv.Atoi(r.Header.Get("X-Sandbox-Gen"))
		if genErr != nil || presentedGen != int(sandboxRow.Gen) {
			logger.Warn("httpapi: opencode-config: rejecting: gen mismatch",
				"presented_gen_header", r.Header.Get("X-Sandbox-Gen"), "sandbox_gen", sandboxRow.Gen)
			writeError(w, http.StatusForbidden, "no usable opencode config for this session")
			return
		}

		if !verifySandboxBearerToken(token, sandboxRow.TokenHash) {
			writeError(w, http.StatusUnauthorized, "invalid sandbox token")
			return
		}

		sessionRow, err := sessions.Get(ctx, sessionID)
		if err != nil {
			logger.Error("httpapi: opencode-config: get session failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		var environmentID *string
		if sessionRow.EnvironmentID.Valid {
			id := sessionRow.EnvironmentID.String()
			environmentID = &id
		}

		rows, err := openCodeConfigs.ListForDelivery(ctx, environmentID)
		if err != nil {
			logger.Error("httpapi: opencode-config: list candidates failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		resp := openCodeConfigDeliveryResponse{}
		for _, row := range rows {
			switch row.Scope {
			case sqlcgen.OpencodeConfigScopeGlobal:
				resp.Global = json.RawMessage(row.Document)
			case sqlcgen.OpencodeConfigScopeEnvironment:
				resp.Environment = json.RawMessage(row.Document)
			}
		}

		writeJSON(w, http.StatusOK, resp)
	}
}
