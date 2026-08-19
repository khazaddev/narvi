// This file (cloudidentityconfigdelivery.go) implements Step 73b's own
// ("cloud identity: sandbox-side consumption + kubeconfig injection",
// §27.3/§27.4) CP-side DELIVERY endpoint for sandbox-agent: POST
// /sessions/{sessionID}/cloud-identity-config (note: no /api prefix,
// exactly like sandbox-secrets/opencode-config/cloud-identity-token
// immediately above -- a sandbox-to-CP endpoint, not a browser-facing
// REST route, §5.2).
//
// # Why this endpoint exists, and why 73a alone did not cover it
//
// 73a shipped cloud_identity_bindings CRUD (browser-facing, a logged-in
// user's own session) and POST /sessions/{id}/cloud-identity-token
// (sandbox-bearer, but requires the CALLER to already know which
// audience to mint against). Neither gives sandbox-agent a way to
// discover, AT BOOT, which bindings even apply to its own session, or
// what role ARN/workload-identity-provider/client-id/env-var-name each
// one declares -- information sandbox-agent needs BEFORE it can mint
// anything or write a single token file. This endpoint is that missing
// discovery step: it resolves and hands back the (kind, audience, params)
// tuple for every binding applicable to this session (mirroring
// sandboxsecretsdelivery.go's own "resolve server-side, hand back
// identifiers" shape), plus this session's own Environment's
// cluster_bindings row, if any -- BOTH pieces of boot-time configuration
// in one round trip, mirroring OpenCodeConfigDelivery's own "bundle
// multiple pieces of boot-time config into one delivery call" precedent
// rather than inventing two near-identical endpoints with two near-
// identical bounded-retry call sites.
//
// Mirrors sandboxsecretsdelivery.go/opencodeconfigdelivery.go's own
// handshake VERBATIM -- identical outcome table (see
// sandboxsecretsdelivery.go's own top doc comment for the numbered list;
// outcomes 1-6 here are byte-for-byte identical). Outcome 7 differs in
// SHAPE only: this endpoint returns EVERY resolved cloud-identity binding
// (one per Kind, environment shadowing global via
// providercredential.Resolve -- mirroring resolveCloudIdentityBindingForAudience's
// own resolution shape, minus the audience pre-filter that endpoint
// applies) plus the session's own cluster binding (0 or 1 row, no
// resolution needed -- §27.4 has no global scope to shadow).
//
// Deliberately NOT gated behind RequireCloudIdentityCapability
// (cloudidentitycapability.go): unlike binding/key CRUD, this is a READ
// of whatever rows already exist -- returning them is harmless (identifiers,
// never secrets, §27.3/§27.4 both), and the cluster_bindings half of the
// response is MEANINGFUL even when cloud identity itself is off (an
// auth_kind='static' cluster binding needs no OIDC issuer at all). The
// capability's own fail-closed enforcement instead happens at the ALREADY-
// gated mint call (cloudidentitytoken.go) sandbox-agent makes for each
// resolved binding -- see cmd/sandbox-agent/cloudidentity.go's own doc
// comment for how a 503 from THAT call degrades per-binding, warn-and-
// continue, never a boot failure.

package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/domain/providercredential"
	"github.com/khazaddev/narvi/internal/domain/sandbox"
	"github.com/khazaddev/narvi/internal/platform"
)

// cloudIdentityConfigBindingResponse is one resolved cloud_identity_bindings
// winner's own wire shape -- deliberately NOT restdtos.CloudIdentityBinding
// (that DTO carries id/scope/scopeTarget/sub, all irrelevant to a
// consumer that only needs kind/audience/params -- this endpoint is a
// sandbox-facing delivery route, outside §6.3's BFF-facing contracts
// scope, exactly like mintCloudIdentityTokenRequest/Response
// (cloudidentitytoken.go) already are).
type cloudIdentityConfigBindingResponse struct {
	Kind     string          `json:"kind"`
	Audience string          `json:"audience"`
	Params   json.RawMessage `json:"params"`
}

// cloudIdentityConfigClusterResponse is the resolved cluster_bindings row's
// own wire shape, when one exists for this session's Environment.
type cloudIdentityConfigClusterResponse struct {
	Name      string          `json:"name"`
	ServerURL *string         `json:"serverUrl,omitempty"`
	CaBundle  *string         `json:"caBundle,omitempty"`
	AuthKind  string          `json:"authKind"`
	Params    json.RawMessage `json:"params"`
}

// cloudIdentityConfigResponse is this endpoint's own invented, documented
// response shape.
type cloudIdentityConfigResponse struct {
	Bindings       []cloudIdentityConfigBindingResponse `json:"bindings"`
	ClusterBinding *cloudIdentityConfigClusterResponse  `json:"clusterBinding,omitempty"`
}

// CloudIdentityConfigDelivery backs POST /sessions/{sessionID}/
// cloud-identity-config -- see this file's own top doc comment for the
// full outcome table and security-posture rationale.
func CloudIdentityConfigDelivery(
	sessions *postgres.SessionStore,
	sandboxes *postgres.SandboxStore,
	bindings *postgres.CloudIdentityBindingStore,
	clusterBindings *postgres.ClusterBindingStore,
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
			logger.Error("httpapi: cloud-identity-config: get sandbox failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if sandbox.IsDeadSandboxStatus(sandbox.State(sandboxRow.Status)) {
			writeError(w, http.StatusGone, "session stopped")
			return
		}

		presentedGen, genErr := strconv.Atoi(r.Header.Get("X-Sandbox-Gen"))
		if genErr != nil || presentedGen != int(sandboxRow.Gen) {
			logger.Warn("httpapi: cloud-identity-config: rejecting: gen mismatch",
				"presented_gen_header", r.Header.Get("X-Sandbox-Gen"), "sandbox_gen", sandboxRow.Gen)
			writeError(w, http.StatusForbidden, "no usable cloud identity config for this session")
			return
		}

		if !verifySandboxBearerToken(token, sandboxRow.TokenHash) {
			writeError(w, http.StatusUnauthorized, "invalid sandbox token")
			return
		}

		sessionRow, err := sessions.Get(ctx, sessionID)
		if err != nil {
			logger.Error("httpapi: cloud-identity-config: get session failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		var environmentID *string
		if sessionRow.EnvironmentID.Valid {
			id := sessionRow.EnvironmentID.String()
			environmentID = &id
		}

		resp := cloudIdentityConfigResponse{}

		rows, err := bindings.ListForSession(ctx, environmentID)
		if err != nil {
			logger.Error("httpapi: cloud-identity-config: list bindings failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		resp.Bindings = resolveCloudIdentityConfigBindings(rows)

		if environmentID != nil {
			clusterRow, err := clusterBindings.Get(ctx, *environmentID)
			if err != nil {
				if !errors.Is(err, pgx.ErrNoRows) {
					logger.Error("httpapi: cloud-identity-config: get cluster binding failed", "error", err)
					writeError(w, http.StatusInternalServerError, "internal error")
					return
				}
				// pgx.ErrNoRows: no cluster configured for this Environment
				// -- resp.ClusterBinding stays nil, an ordinary, expected
				// outcome, never logged or treated as a failure.
			} else {
				params := clusterRow.Params
				if params == nil {
					params = emptyJSONObject
				}
				resp.ClusterBinding = &cloudIdentityConfigClusterResponse{
					Name:      clusterRow.Name,
					ServerURL: clusterRow.ServerUrl,
					CaBundle:  clusterRow.CaBundle,
					AuthKind:  string(clusterRow.AuthKind),
					Params:    json.RawMessage(params),
				}
			}
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

// resolveCloudIdentityConfigBindings groups rows by Kind and resolves the
// environment-vs-global winner for each via providercredential.Resolve --
// mirrors resolveCloudIdentityBindingForAudience's own identical
// resolution shape (cloudidentitytoken.go), applied per-Kind instead of
// against one pre-filtered audience. Returns at most 4 entries (one per
// cloudidentity.Kind), sorted by the caller's own SQL ORDER BY kind
// (ListCloudIdentityBindingsForSession), so output order is deterministic
// without this function needing its own sort.
func resolveCloudIdentityConfigBindings(rows []sqlcgen.CloudIdentityBinding) []cloudIdentityConfigBindingResponse {
	byKind := make(map[sqlcgen.CloudIdentityBindingKind][]providercredential.Candidate[sqlcgen.CloudIdentityBinding])
	var kindOrder []sqlcgen.CloudIdentityBindingKind
	for _, row := range rows {
		if _, seen := byKind[row.Kind]; !seen {
			kindOrder = append(kindOrder, row.Kind)
		}
		byKind[row.Kind] = append(byKind[row.Kind], providercredential.Candidate[sqlcgen.CloudIdentityBinding]{
			Scope: cloudIdentityBindingScopeToDomainScope(row.Scope),
			Value: row,
		})
	}

	out := make([]cloudIdentityConfigBindingResponse, 0, len(kindOrder))
	for _, kind := range kindOrder {
		winner, ok := providercredential.Resolve(byKind[kind])
		if !ok {
			continue
		}
		params := winner.Params
		if params == nil {
			params = emptyJSONObject
		}
		out = append(out, cloudIdentityConfigBindingResponse{
			Kind:     string(winner.Kind),
			Audience: winner.Audience,
			Params:   json.RawMessage(params),
		})
	}
	return out
}
