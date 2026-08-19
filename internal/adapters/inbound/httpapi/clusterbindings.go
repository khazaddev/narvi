// This file (clusterbindings.go) implements Step 73b's own ("cloud
// identity: sandbox-side consumption + kubeconfig injection", §27.4)
// CP-side MANAGEMENT surface over cluster_bindings (migrations/
// 000094_cluster_bindings.up.sql):
//
//   - GET/PUT/DELETE /api/environments/{environmentID}/cluster-binding (authz.ActionManageClusterBindings, maintainer+)
//
// Unlike cloudidentitybindings.go (a list-of-many-rows CRUD over up to 4
// kinds per scope target), this is a SINGLETON resource per Environment
// (§27.4: "one cluster per Environment in v1") with NO global scope at
// all -- mirrors opencodeconfig.go's own GET/PUT/DELETE-over-a-single-row
// shape exactly, narrowed further to one route group (no global-scoped
// sibling), since §27.4 names no global fallback for a deployment target.
// PUT is create-or-replace (upsert) -- there is no separate POST/
// id-based-PUT pair, since a caller never needs to learn or pass an id
// for a resource unique per Environment.
//
// # No secret material here -- unlike provider_credentials/sandbox_secrets
//
// A binding's own name/serverUrl/caBundle/params are identifiers, never
// secrets (§27.4's own "params JSONB" shape, mirroring
// cloud_identity_bindings.params exactly), so GET returns every field in
// full, never masked -- see CloudIdentityBinding's own identical
// precedent. The ONE genuinely secret-shaped thing this feature ever
// touches -- an uploaded static kubeconfig's own file content -- is never
// stored here at all: authKind='static' only ever stores a REFERENCE
// (params.secretName) to a Step 72 sandbox_secrets row, written/read
// through THAT table's own encrypted-at-rest write path, not this one.
//
// # Validation at save: internal/domain/clusterbinding.Validate + ValidateParams
//
// putClusterBinding calls both BEFORE ever reaching storage -- Validate
// confirms name/authKind/serverUrl/caBundle's own structural shape (400
// on a violation), ValidateParams confirms params carries the ONE
// required key each auth rung actually needs (cloud/clientId/secretName --
// 400 on a missing/malformed one). Both are mirrored, redundantly, by
// migrations/000094_cluster_bindings.up.sql's own CHECK constraint for
// the serverUrl/caBundle half (defense in depth, exactly like
// cloudidentitybindings.go's own ValidateBinding/CHECK pairing) -- params'
// own per-rung required key has no equivalent DB-level check (JSONB
// content is opaque to a CHECK constraint the way a fixed column never
// is), so ValidateParams is the ONLY gate for that half.

package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/domain/authz"
	"github.com/khazaddev/narvi/internal/domain/clusterbinding"
	"github.com/khazaddev/narvi/internal/platform"
)

// clusterBindingToDTO converts a stored sqlcgen.ClusterBinding row into
// its REST wire shape.
func clusterBindingToDTO(row sqlcgen.ClusterBinding) restdtos.ClusterBinding {
	params := row.Params
	if params == nil {
		params = emptyJSONObject
	}
	return restdtos.ClusterBinding{
		EnvironmentId: row.EnvironmentID,
		Name:          row.Name,
		ServerUrl:     restdtos.ClusterBindingServerUrl(row.ServerUrl),
		CaBundle:      restdtos.ClusterBindingCaBundle(row.CaBundle),
		AuthKind:      restdtos.ClusterBindingAuthKind(row.AuthKind),
		Params:        json.RawMessage(params),
		CreatedAt:     row.CreatedAt.Time,
		UpdatedAt:     row.UpdatedAt.Time,
	}
}

// GetEnvironmentClusterBinding backs GET /api/environments/
// {environmentID}/cluster-binding -- 404 when nothing is configured yet
// for this Environment (§27.4's own "at most one... per environment",
// never "exactly one").
func GetEnvironmentClusterBinding(store *postgres.ClusterBindingStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, authz.ActionManageClusterBindings, authz.Resource{}) {
			return
		}
		environmentID, ok := environmentIDFromRoute(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		logger := platform.Logger(ctx)

		row, err := store.Get(ctx, environmentID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "no cluster binding configured for this environment")
				return
			}
			logger.Error("httpapi: get cluster binding failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, http.StatusOK, clusterBindingToDTO(row))
	}
}

// PutEnvironmentClusterBinding backs PUT /api/environments/
// {environmentID}/cluster-binding -- create-or-replace.
func PutEnvironmentClusterBinding(store *postgres.ClusterBindingStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, authz.ActionManageClusterBindings, authz.Resource{}) {
			return
		}
		environmentID, ok := environmentIDFromRoute(w, r)
		if !ok {
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		var req restdtos.PutClusterBindingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed request body")
			return
		}

		authKind := clusterbinding.AuthKind(req.AuthKind)
		var serverURL, caBundle string
		if req.ServerUrl != nil {
			serverURL = *req.ServerUrl
		}
		if req.CaBundle != nil {
			caBundle = *req.CaBundle
		}
		if err := clusterbinding.Validate(req.Name, authKind, serverURL, caBundle); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		params := emptyJSONObject
		if req.Params != nil {
			params = *req.Params
		}
		if err := validateJSONObject(params); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := clusterbinding.ValidateParams(authKind, params); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		ctx := r.Context()
		logger := platform.Logger(ctx)

		// serverUrl/caBundle are stored NULL for authKind='static' (never
		// the empty string) -- Validate above already confirmed that rung
		// tolerates either being blank/absent; nil-ing them out here keeps
		// a static binding's own stored row from carrying stale/misleading
		// values a later cloud/oidc PUT never actually replaces via
		// ON CONFLICT (a fresh upsert always overwrites both columns
		// wholesale, so this is about what a STATIC row itself stores, not
		// about a partial update).
		var storedServerURL, storedCABundle *string
		if authKind != clusterbinding.AuthKindStatic {
			storedServerURL = req.ServerUrl
			storedCABundle = req.CaBundle
		}

		row, err := store.Upsert(ctx, environmentID, req.Name, sqlcgen.ClusterBindingAuthKind(authKind), storedServerURL, storedCABundle, params)
		if err != nil {
			logger.Error("httpapi: upsert cluster binding failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, http.StatusOK, clusterBindingToDTO(row))
	}
}

// DeleteEnvironmentClusterBinding backs DELETE /api/environments/
// {environmentID}/cluster-binding.
func DeleteEnvironmentClusterBinding(store *postgres.ClusterBindingStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, authz.ActionManageClusterBindings, authz.Resource{}) {
			return
		}
		environmentID, ok := environmentIDFromRoute(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		logger := platform.Logger(ctx)

		affected, err := store.Delete(ctx, environmentID)
		if err != nil {
			logger.Error("httpapi: delete cluster binding failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if affected == 0 {
			writeError(w, http.StatusNotFound, "no cluster binding configured for this environment")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
