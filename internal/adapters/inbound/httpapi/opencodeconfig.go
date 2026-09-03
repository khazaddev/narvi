// This file (opencodeconfig.go) implements §27.1's own ("sandbox
// secrets & opencode config", §27.2) CP-side MANAGEMENT surface over
// opencode_configs (migrations/000091_opencode_configs.up.sql):
//
//   - GET/PUT/DELETE /api/environments/{environmentID}/opencode-config (authz.ActionManageEnvSecrets, admin+maintainer)
//   - GET/PUT/DELETE /api/opencode-config                              (authz.ActionManageGlobalSecrets, admin only)
//
// Unlike providercredentials.go/sandboxsecrets.go, this is a SINGLETON
// resource per scope target (§27.2: "at most one global row, one per
// environment") -- mirrors reposettings.go's own GET/PUT-over-a-single-
// row-per-repo shape more closely than the list-of-many-rows CRUD those
// 2 files implement. PUT is create-or-replace (upsert) -- there is no
// separate POST/id-based-PUT pair the way ProviderCredential/
// SandboxSecret use, since a caller never needs to learn or pass an id
// for a resource it already knows is unique per scope target.
//
// RBAC reuses the SAME 2 already-reserved actions provider_credentials/
// sandbox_secrets use for their own environment/global route groups
// (§27.1's "§25.1's idioms reused throughout" extended to §27.2) --
// deliberately NOT a new OpenCode-config-specific action: §27.2 itself
// describes this surface's RBAC as "global scope admin-only (the §13.3
// row that owns integrations/global secrets); environment scope
// maintainer+ (the row that owns environments/env secrets)" -- i.e. the
// SAME 2 rows ActionManageGlobalSecrets/ActionManageEnvSecrets already
// gate, not a third row this Step would have to invent and add to the
// §13.3 matrix.
//
// # document is returned in full -- NOT write-only
//
// Unlike every other secret-storage handler in this package,
// opencode_configs holds NO secret material (that table's own top
// migration comment: "PLAINTEXT JSONB, deliberately... configuration
// users read and edit in Settings"), so GET returns the FULL document,
// never a masked placeholder. A value that IS secret-shaped belongs in
// sandbox_secrets and is referenced from document via OpenCode's own
// `{env:VAR}` substitution -- this handler never inspects document
// deeply enough to tell the difference, by design (§27.2: "nothing
// deeper, because OpenCode's own schema drifts with its version").
//
// # Validation at save: "parses as a JSON object", nothing deeper
//
// putOpenCodeConfig confirms document unmarshals as a JSON object (a
// bare `{}`-shaped value, not an array/string/number/bool/null) before
// storing it -- restdtos.PutOpenCodeConfigRequest.Document's own
// underlying Go type is json.RawMessage specifically so this handler
// (not the generic JSON decoder) makes that one check, then stores the
// raw bytes unchanged. No deeper schema validation is performed -- see
// this file's own doc comment above for why.

package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/narvidev/narvi/contracts/gen/go/restdtos"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/domain/authz"
	"github.com/narvidev/narvi/internal/platform"
)

// maxOpenCodeConfigDocumentBytes bounds an opencode_configs document --
// §27.2's own "bounded size" validation rule. Not specified in the plan;
// chosen as maxRequestBodyBytes itself (1 MiB) since the whole request
// body is already bounded to that via http.MaxBytesReader below -- a
// separate, smaller document-only cap would be a second, redundant bound
// on the SAME 1 MiB ceiling for no real gain (an opencode.json document
// is a handful of KB in every realistic case; nothing about this handler
// needs a tighter number than the generic request-body ceiling already
// gives it).
const maxOpenCodeConfigDocumentBytes = maxRequestBodyBytes

// openCodeConfigToDTO converts a stored sqlcgen.OpencodeConfig row into
// its REST wire shape -- unlike providerCredentialToDTO/
// sandboxSecretToDTO, this returns document in FULL (see this file's own
// top doc comment for why).
func openCodeConfigToDTO(row sqlcgen.OpencodeConfig) restdtos.OpenCodeConfig {
	return restdtos.OpenCodeConfig{
		Scope:       restdtos.OpenCodeConfigScope(row.Scope),
		ScopeTarget: restdtos.OpenCodeConfigScopeTarget(row.ScopeTargetID),
		Document:    json.RawMessage(row.Document),
		CreatedAt:   row.CreatedAt.Time,
		UpdatedAt:   row.UpdatedAt.Time,
	}
}

// decodeOpenCodeConfigDocument reads and validates a PutOpenCodeConfigRequest
// body, confirming Document parses as a JSON object (§27.2: "parses as a
// JSON object, bounded size -- nothing deeper"). Writes its own 400 and
// returns ok=false on any failure.
func decodeOpenCodeConfigDocument(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxOpenCodeConfigDocumentBytes)
	var req restdtos.PutOpenCodeConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request body")
		return nil, false
	}

	// req.Document is already known to be well-formed JSON (the outer
	// json.Unmarshal above would have failed otherwise, since
	// json.RawMessage's own UnmarshalJSON validates its input) -- this
	// second, narrower parse confirms it is specifically an OBJECT, not
	// merely valid JSON of any shape (an array/string/number/bool/null
	// document would silently break OpenCode's own loader, which expects
	// a top-level object).
	//
	// Deliberately does NOT rely on json.Unmarshal(req.Document,
	// &map[string]any{}) alone: unmarshaling a JSON `null` into ANY
	// pointer/map/slice target SUCCEEDS in Go's own encoding/json (it
	// simply leaves the target at its zero value, no error) -- so
	// {"document":null} would silently pass a map-only check. json.
	// Unmarshal into json.RawMessage's own %T check below is used
	// specifically to reject that case: the FIRST non-whitespace token of
	// req.Document must decode as exactly one map[string]any -- checked
	// by requiring both that Unmarshal succeeds AND that req.Document
	// itself is non-empty and does not start with 'n' (the only way a
	// top-level `null` can begin) before ever calling Unmarshal at all.
	trimmed := bytes.TrimSpace(req.Document)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		writeError(w, http.StatusBadRequest, "document must be a JSON object")
		return nil, false
	}
	var asObject map[string]any
	if err := json.Unmarshal(req.Document, &asObject); err != nil {
		writeError(w, http.StatusBadRequest, "document must be a JSON object")
		return nil, false
	}

	return []byte(req.Document), true
}

// --- Environment-scoped route group: /api/environments/{environmentID}/opencode-config ---

// GetEnvironmentOpenCodeConfig backs GET /api/environments/
// {environmentID}/opencode-config -- 404 when nothing is configured yet
// for this Environment (§27.2's own "at most one... per environment",
// never "exactly one").
func GetEnvironmentOpenCodeConfig(store *postgres.OpenCodeConfigStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, authz.ActionManageEnvSecrets, authz.Resource{}) {
			return
		}
		environmentID, ok := environmentIDFromRoute(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		logger := platform.Logger(ctx)

		row, err := store.GetEnvironment(ctx, environmentID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "no opencode config configured for this environment")
				return
			}
			logger.Error("httpapi: get environment opencode config failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, http.StatusOK, openCodeConfigToDTO(row))
	}
}

// PutEnvironmentOpenCodeConfig backs PUT /api/environments/
// {environmentID}/opencode-config -- create-or-replace.
func PutEnvironmentOpenCodeConfig(store *postgres.OpenCodeConfigStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, authz.ActionManageEnvSecrets, authz.Resource{}) {
			return
		}
		environmentID, ok := environmentIDFromRoute(w, r)
		if !ok {
			return
		}
		document, ok := decodeOpenCodeConfigDocument(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		logger := platform.Logger(ctx)

		row, err := store.UpsertEnvironment(ctx, environmentID, document)
		if err != nil {
			logger.Error("httpapi: upsert environment opencode config failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, http.StatusOK, openCodeConfigToDTO(row))
	}
}

// DeleteEnvironmentOpenCodeConfigHandler backs DELETE /api/environments/
// {environmentID}/opencode-config.
func DeleteEnvironmentOpenCodeConfigHandler(store *postgres.OpenCodeConfigStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, authz.ActionManageEnvSecrets, authz.Resource{}) {
			return
		}
		environmentID, ok := environmentIDFromRoute(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		logger := platform.Logger(ctx)

		affected, err := store.DeleteEnvironment(ctx, environmentID)
		if err != nil {
			logger.Error("httpapi: delete environment opencode config failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if affected == 0 {
			writeError(w, http.StatusNotFound, "no opencode config configured for this environment")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- Global-scoped route group: /api/opencode-config ---

// GetGlobalOpenCodeConfig backs GET /api/opencode-config.
func GetGlobalOpenCodeConfig(store *postgres.OpenCodeConfigStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, authz.ActionManageGlobalSecrets, authz.Resource{}) {
			return
		}
		ctx := r.Context()
		logger := platform.Logger(ctx)

		row, err := store.GetGlobal(ctx)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "no global opencode config configured")
				return
			}
			logger.Error("httpapi: get global opencode config failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, http.StatusOK, openCodeConfigToDTO(row))
	}
}

// PutGlobalOpenCodeConfig backs PUT /api/opencode-config -- create-or-replace.
func PutGlobalOpenCodeConfig(store *postgres.OpenCodeConfigStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, authz.ActionManageGlobalSecrets, authz.Resource{}) {
			return
		}
		document, ok := decodeOpenCodeConfigDocument(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		logger := platform.Logger(ctx)

		row, err := store.UpsertGlobal(ctx, document)
		if err != nil {
			logger.Error("httpapi: upsert global opencode config failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, http.StatusOK, openCodeConfigToDTO(row))
	}
}

// DeleteGlobalOpenCodeConfigHandler backs DELETE /api/opencode-config.
func DeleteGlobalOpenCodeConfigHandler(store *postgres.OpenCodeConfigStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, authz.ActionManageGlobalSecrets, authz.Resource{}) {
			return
		}
		ctx := r.Context()
		logger := platform.Logger(ctx)

		affected, err := store.DeleteGlobal(ctx)
		if err != nil {
			logger.Error("httpapi: delete global opencode config failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if affected == 0 {
			writeError(w, http.StatusNotFound, "no global opencode config configured")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
