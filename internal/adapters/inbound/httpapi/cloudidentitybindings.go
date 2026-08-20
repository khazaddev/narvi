// This file (cloudidentitybindings.go) implements §27.3's own ("cloud
// identity: OIDC issuer, bindings, minting", §27.3) CP-side MANAGEMENT
// surface over cloud_identity_bindings (migrations/
// 000093_cloud_identity_bindings.up.sql) -- mirrors providercredentials.
// go's own "GET/POST/PUT/DELETE behind auth.Middleware, one route group
// per scope" shape closely, narrowed to this table's own 2 scopes
// (deliberately no repo scope, §27.3) and ONE shared RBAC action for
// both (unlike provider_credentials' 3-way maintainer/maintainer/admin
// split -- see authz.ActionManageCloudIdentityBindings' own doc comment
// for why):
//
//   - /api/environments/{environmentID}/cloud-identity-bindings (authz.ActionManageCloudIdentityBindings, maintainer+)
//   - /api/cloud-identity-bindings                               (authz.ActionManageCloudIdentityBindings, maintainer+; scope is always ScopeGlobal)
//
// Each group exposes POST (create) / GET (list) / PUT :bindingID (rotate
// audience+params) / DELETE :bindingID -- scope and scopeTargetID are
// ALWAYS implied by which route group a request hit, never accepted as a
// body field, mirroring providercredentials.go's own identical
// "URL and body can never disagree about scope" invariant.
//
// # No secret material here -- unlike provider_credentials/sandbox_secrets
//
// A binding's own audience/params are identifiers, never secrets (§27.3),
// so unlike ProviderCredential's maskedValue/never-returned-value
// convention, this file returns audience/params IN FULL, on every route,
// including list -- there is nothing here that needs write-only handling.
//
// # This Step's own gap-4 resolution: the response surfaces the exact
// `sub` string
//
// cloudIdentityBindingToDTO (below) computes and returns `sub`
// (narvi:environment:<environment_id>, internal/domain/cloudidentity.Sub)
// directly on every environment-scoped binding's own DTO -- a customer
// configuring their cloud-side trust policy never has to construct this
// string themselves from a raw environment id; they copy it verbatim from
// this API's own response. Global-scoped bindings return sub=null (see
// CloudIdentityBinding.sub's own schema doc comment for why there is no
// single string to surface for that scope).
//
// # This Step's own gap-3 resolution: azure+global refused, structurally
//
// createCloudIdentityBinding calls internal/domain/cloudidentity.
// ValidateBinding BEFORE ever reaching storage -- a kind=azure request
// against the GLOBAL route group is rejected 400, never silently
// accepted (see that function's own doc comment, and migrations/
// 000093_cloud_identity_bindings.up.sql's own matching CHECK constraint
// for defense in depth).

package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/domain/authz"
	"github.com/khazaddev/narvi/internal/domain/cloudidentity"
	"github.com/khazaddev/narvi/internal/domain/providercredential"
	"github.com/khazaddev/narvi/internal/platform"
)

// emptyJSONObject is the default params value CreateCloudIdentityBinding/
// UpdateCloudIdentityBinding store when the request omits params
// entirely -- matches the DB column's own DEFAULT '{}'::jsonb.
var emptyJSONObject = json.RawMessage(`{}`)

// cloudIdentityBindingScopeToDomainScope converts a stored sqlcgen.
// CloudIdentityBindingScope into internal/domain/providercredential.Scope
// -- the vocabulary internal/domain/cloudidentity.ValidateBinding and
// providercredential.Resolve both speak (see internal/domain/
// cloudidentity's own scope.go doc comment for why THIS table reuses
// providercredential's Scope type rather than inventing a parallel one).
func cloudIdentityBindingScopeToDomainScope(s sqlcgen.CloudIdentityBindingScope) providercredential.Scope {
	if s == sqlcgen.CloudIdentityBindingScopeGlobal {
		return providercredential.ScopeGlobal
	}
	return providercredential.ScopeEnvironment
}

// validateJSONObject reports whether raw parses as a JSON object (never
// an array/string/number/bool/null at the top level) -- mirrors
// OpenCodeConfig's own "must parse as a JSON object, nothing deeper
// checked server-side" validation posture (opencodeconfig.go).
func validateJSONObject(raw json.RawMessage) error {
	var v map[string]json.RawMessage
	if err := json.Unmarshal(raw, &v); err != nil {
		return errors.New("params must be a JSON object")
	}
	return nil
}

// parseCloudIdentityBindingID parses chi's own "bindingID" URL path param
// as a UUID.
func parseCloudIdentityBindingID(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	return parseUUIDParam(w, r, "bindingID", "malformed cloud identity binding id")
}

// cloudIdentityBindingToDTO converts a stored sqlcgen.CloudIdentityBinding
// row into its REST wire shape -- see this file's own top doc comment for
// the sub computation (gap 4).
func cloudIdentityBindingToDTO(row sqlcgen.CloudIdentityBinding) restdtos.CloudIdentityBinding {
	var sub *string
	if row.Scope == sqlcgen.CloudIdentityBindingScopeEnvironment && row.ScopeTargetID != nil {
		s := cloudidentity.Sub(*row.ScopeTargetID)
		sub = &s
	}
	params := row.Params
	if params == nil {
		params = emptyJSONObject
	}
	return restdtos.CloudIdentityBinding{
		Id:          row.ID.String(),
		Scope:       restdtos.CloudIdentityBindingScope(row.Scope),
		ScopeTarget: restdtos.CloudIdentityBindingScopeTarget(row.ScopeTargetID),
		Kind:        restdtos.CloudIdentityBindingKind(row.Kind),
		Audience:    row.Audience,
		Params:      params,
		Sub:         sub,
		CreatedAt:   row.CreatedAt.Time,
		UpdatedAt:   row.UpdatedAt.Time,
	}
}

// getCloudIdentityBindingInScope fetches id's own row and confirms it
// actually belongs to (scope, scopeTargetID) -- the SAME "cross-scope row
// confusion" (IDOR-shaped risk) closure providercredentials.go's own
// getProviderCredentialInScope documents and closes, applied here
// identically: a maintainer who only holds this action for one
// Environment must not be able to PUT/DELETE a DIFFERENT Environment's
// (or the global) binding by guessing/learning its id. Returns
// pgx.ErrNoRows (unwrapped) on either a missing row or a scope/target
// mismatch -- never a distinguishing error.
func getCloudIdentityBindingInScope(w http.ResponseWriter, r *http.Request, store *postgres.CloudIdentityBindingStore, id pgtype.UUID, scope sqlcgen.CloudIdentityBindingScope, scopeTargetID *string) (sqlcgen.CloudIdentityBinding, bool) {
	ctx := r.Context()
	logger := platform.Logger(ctx)

	row, err := store.Get(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "cloud identity binding not found")
			return sqlcgen.CloudIdentityBinding{}, false
		}
		logger.Error("httpapi: get cloud identity binding failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return sqlcgen.CloudIdentityBinding{}, false
	}
	if row.Scope != scope || !scopeTargetIDEqual(row.ScopeTargetID, scopeTargetID) {
		writeError(w, http.StatusNotFound, "cloud identity binding not found")
		return sqlcgen.CloudIdentityBinding{}, false
	}
	return row, true
}

// createCloudIdentityBinding is the shared core POST handler body.
func createCloudIdentityBinding(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, store *postgres.CloudIdentityBindingStore, auditLog *postgres.AuditLogStore, scope sqlcgen.CloudIdentityBindingScope, resolveScope scopeTargetResolver) {
	if !authorize(w, r, authz.ActionManageCloudIdentityBindings, authz.Resource{}) {
		return
	}
	scopeTargetID, ok := resolveScope(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	logger := platform.Logger(ctx)

	actorUserID, ok := authenticatedUserID(w, r)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	var req restdtos.CreateCloudIdentityBindingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request body")
		return
	}

	kind := cloudidentity.Kind(req.Kind)
	domainScope := cloudIdentityBindingScopeToDomainScope(scope)
	if err := cloudidentity.ValidateBinding(kind, domainScope); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := cloudidentity.ValidateAudience(req.Audience); err != nil {
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

	tx, err := pool.Begin(ctx)
	if err != nil {
		logger.Error("httpapi: create cloud identity binding: begin tx failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	created, err := store.WithTx(tx).Create(ctx, scope, scopeTargetID, sqlcgen.CloudIdentityBindingKind(kind), req.Audience, params)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a binding for this kind already exists at this scope -- update it instead")
			return
		}
		logger.Error("httpapi: create cloud identity binding failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// audit_log records binding CRUD (§27.3: "audit_log records binding
	// CRUD, not each 5-minute refresh -- proportionate, or the audit log
	// becomes noise") -- written in the SAME transaction as the change
	// (§13.3), never a best-effort side write.
	if err := recordAuditLog(ctx, auditLog.WithTx(tx), actorUserID, "cloud_identity_binding.created", "cloud_identity_binding", created.ID.String(), map[string]any{
		"scope": string(created.Scope),
		"kind":  string(created.Kind),
	}); err != nil {
		logger.Error("httpapi: create cloud identity binding: record audit log failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		logger.Error("httpapi: create cloud identity binding: commit tx failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, cloudIdentityBindingToDTO(created))
}

// listCloudIdentityBindings is the shared core GET (list) handler body.
func listCloudIdentityBindings(w http.ResponseWriter, r *http.Request, store *postgres.CloudIdentityBindingStore, scope sqlcgen.CloudIdentityBindingScope, resolveScope scopeTargetResolver) {
	if !authorize(w, r, authz.ActionManageCloudIdentityBindings, authz.Resource{}) {
		return
	}
	scopeTargetID, ok := resolveScope(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	logger := platform.Logger(ctx)

	rows, err := store.ListByScope(ctx, scope, scopeTargetID)
	if err != nil {
		logger.Error("httpapi: list cloud identity bindings failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	dtos := make([]restdtos.CloudIdentityBinding, 0, len(rows))
	for _, row := range rows {
		dtos = append(dtos, cloudIdentityBindingToDTO(row))
	}
	writeJSON(w, http.StatusOK, restdtos.ListCloudIdentityBindingsResponse{CloudIdentityBindings: dtos})
}

// updateCloudIdentityBinding is the shared core PUT (rotate audience/
// params) handler body.
func updateCloudIdentityBinding(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, store *postgres.CloudIdentityBindingStore, auditLog *postgres.AuditLogStore, scope sqlcgen.CloudIdentityBindingScope, resolveScope scopeTargetResolver) {
	if !authorize(w, r, authz.ActionManageCloudIdentityBindings, authz.Resource{}) {
		return
	}
	scopeTargetID, scopeOK := resolveScope(w, r)
	if !scopeOK {
		return
	}
	ctx := r.Context()
	logger := platform.Logger(ctx)

	actorUserID, ok := authenticatedUserID(w, r)
	if !ok {
		return
	}

	id, ok := parseCloudIdentityBindingID(w, r)
	if !ok {
		return
	}

	if _, ok := getCloudIdentityBindingInScope(w, r, store, id, scope, scopeTargetID); !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	var req restdtos.UpdateCloudIdentityBindingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request body")
		return
	}

	if err := cloudidentity.ValidateAudience(req.Audience); err != nil {
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

	tx, err := pool.Begin(ctx)
	if err != nil {
		logger.Error("httpapi: update cloud identity binding: begin tx failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	updated, err := store.WithTx(tx).Update(ctx, id, req.Audience, params)
	if err != nil {
		logger.Error("httpapi: update cloud identity binding failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := recordAuditLog(ctx, auditLog.WithTx(tx), actorUserID, "cloud_identity_binding.updated", "cloud_identity_binding", updated.ID.String(), map[string]any{
		"scope": string(updated.Scope),
		"kind":  string(updated.Kind),
	}); err != nil {
		logger.Error("httpapi: update cloud identity binding: record audit log failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		logger.Error("httpapi: update cloud identity binding: commit tx failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, cloudIdentityBindingToDTO(updated))
}

// deleteCloudIdentityBinding is the shared core DELETE handler body.
func deleteCloudIdentityBinding(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, store *postgres.CloudIdentityBindingStore, auditLog *postgres.AuditLogStore, scope sqlcgen.CloudIdentityBindingScope, resolveScope scopeTargetResolver) {
	if !authorize(w, r, authz.ActionManageCloudIdentityBindings, authz.Resource{}) {
		return
	}
	scopeTargetID, scopeOK := resolveScope(w, r)
	if !scopeOK {
		return
	}
	ctx := r.Context()
	logger := platform.Logger(ctx)

	actorUserID, ok := authenticatedUserID(w, r)
	if !ok {
		return
	}

	id, ok := parseCloudIdentityBindingID(w, r)
	if !ok {
		return
	}

	existing, ok := getCloudIdentityBindingInScope(w, r, store, id, scope, scopeTargetID)
	if !ok {
		return
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		logger.Error("httpapi: delete cloud identity binding: begin tx failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := store.WithTx(tx).Delete(ctx, id); err != nil {
		logger.Error("httpapi: delete cloud identity binding failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := recordAuditLog(ctx, auditLog.WithTx(tx), actorUserID, "cloud_identity_binding.deleted", "cloud_identity_binding", existing.ID.String(), map[string]any{
		"scope": string(existing.Scope),
		"kind":  string(existing.Kind),
	}); err != nil {
		logger.Error("httpapi: delete cloud identity binding: record audit log failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		logger.Error("httpapi: delete cloud identity binding: commit tx failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Environment-scoped route group: /api/environments/{environmentID}/cloud-identity-bindings ---

// CreateEnvironmentCloudIdentityBinding backs POST /api/environments/
// {environmentID}/cloud-identity-bindings.
func CreateEnvironmentCloudIdentityBinding(pool *pgxpool.Pool, store *postgres.CloudIdentityBindingStore, auditLog *postgres.AuditLogStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		createCloudIdentityBinding(w, r, pool, store, auditLog, sqlcgen.CloudIdentityBindingScopeEnvironment, environmentScopeTarget)
	}
}

// ListEnvironmentCloudIdentityBindings backs GET /api/environments/
// {environmentID}/cloud-identity-bindings.
func ListEnvironmentCloudIdentityBindings(store *postgres.CloudIdentityBindingStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		listCloudIdentityBindings(w, r, store, sqlcgen.CloudIdentityBindingScopeEnvironment, environmentScopeTarget)
	}
}

// UpdateEnvironmentCloudIdentityBinding backs PUT /api/environments/
// {environmentID}/cloud-identity-bindings/{bindingID}.
func UpdateEnvironmentCloudIdentityBinding(pool *pgxpool.Pool, store *postgres.CloudIdentityBindingStore, auditLog *postgres.AuditLogStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		updateCloudIdentityBinding(w, r, pool, store, auditLog, sqlcgen.CloudIdentityBindingScopeEnvironment, environmentScopeTarget)
	}
}

// DeleteEnvironmentCloudIdentityBinding backs DELETE /api/environments/
// {environmentID}/cloud-identity-bindings/{bindingID}.
func DeleteEnvironmentCloudIdentityBinding(pool *pgxpool.Pool, store *postgres.CloudIdentityBindingStore, auditLog *postgres.AuditLogStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		deleteCloudIdentityBinding(w, r, pool, store, auditLog, sqlcgen.CloudIdentityBindingScopeEnvironment, environmentScopeTarget)
	}
}

// --- Global-scoped route group: /api/cloud-identity-bindings ---
//
// scopeTargetID is always nil here -- there is no URL segment to derive
// it from, matching cloud_identity_bindings' own CHECK constraint
// (scope=global requires scope_target_id NULL).

// CreateGlobalCloudIdentityBinding backs POST /api/cloud-identity-bindings.
func CreateGlobalCloudIdentityBinding(pool *pgxpool.Pool, store *postgres.CloudIdentityBindingStore, auditLog *postgres.AuditLogStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		createCloudIdentityBinding(w, r, pool, store, auditLog, sqlcgen.CloudIdentityBindingScopeGlobal, globalScopeTarget)
	}
}

// ListGlobalCloudIdentityBindings backs GET /api/cloud-identity-bindings.
func ListGlobalCloudIdentityBindings(store *postgres.CloudIdentityBindingStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		listCloudIdentityBindings(w, r, store, sqlcgen.CloudIdentityBindingScopeGlobal, globalScopeTarget)
	}
}

// UpdateGlobalCloudIdentityBinding backs PUT /api/cloud-identity-bindings/
// {bindingID}.
func UpdateGlobalCloudIdentityBinding(pool *pgxpool.Pool, store *postgres.CloudIdentityBindingStore, auditLog *postgres.AuditLogStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		updateCloudIdentityBinding(w, r, pool, store, auditLog, sqlcgen.CloudIdentityBindingScopeGlobal, globalScopeTarget)
	}
}

// DeleteGlobalCloudIdentityBinding backs DELETE /api/cloud-identity-bindings/
// {bindingID}.
func DeleteGlobalCloudIdentityBinding(pool *pgxpool.Pool, store *postgres.CloudIdentityBindingStore, auditLog *postgres.AuditLogStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		deleteCloudIdentityBinding(w, r, pool, store, auditLog, sqlcgen.CloudIdentityBindingScopeGlobal, globalScopeTarget)
	}
}
