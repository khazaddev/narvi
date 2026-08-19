// This file (sandboxsecrets.go) implements Step 72's own ("sandbox
// secrets & opencode config", §27.1) CP-side MANAGEMENT surface over
// sandbox_secrets (migrations/000090_sandbox_secrets.up.sql) -- this
// codebase's SECOND generic secret-storage table. Mirrors
// providercredentials.go's own shape almost exactly (that file's own top
// doc comment describes the full reasoning this file reuses verbatim --
// §27.1's own "Step 53's idioms reused throughout" instruction), swapping
// ProviderCredentialStore/ProviderCredentialScope for
// SandboxSecretStore/SandboxSecretScope and provider (a closed ENUM) for
// name (a user-chosen, validated string):
//
//   - /api/repos/{owner}/{repo}/sandbox-secrets        (authz.ActionManageRepoSecrets, admin+maintainer)
//   - /api/environments/{environmentID}/sandbox-secrets (authz.ActionManageEnvSecrets, admin+maintainer)
//   - /api/sandbox-secrets                              (authz.ActionManageGlobalSecrets, admin only; scope is always ScopeGlobal)
//
// Each group exposes POST (create) / GET (list) / PUT :secretID (rotate
// the value) / DELETE :secretID -- scope and scopeTargetID are ALWAYS
// implied by which route group a request hit (repo_full_name from the
// URL, the environment id from the URL, or nothing at all for global),
// never accepted as a body field -- the SAME reasoning
// providercredentials.go's own top doc comment gives, word for word.
// Deliberately NO automation-scoped route group -- §27.1's own
// "schema-only" carve-out for ScopeAutomation (migrations/
// 000090_sandbox_secrets.up.sql's own top comment): no CRUD surface
// exists for it in this Step, mirroring how providercredentials.go itself
// has no user-scoped route group (that scope is managed through a
// completely separate flow, chatgptlink.go).
//
// # Name validation, fail-closed, at create time only
//
// A new row's name is validated via internal/domain/sandboxsecret.
// ValidateName BEFORE the encrypt-and-insert -- POSIX env-var shape, the
// NARVI_* namespace rejected, and every name providercredential.
// EnvVarNames already owns rejected too (§27.1's own "one owning
// mechanism per env-var name" rule). Name is immutable once created (like
// provider_credentials' own provider column) -- the PUT route only
// rotates value, so it never re-validates name.
//
// # Write-only secret value (never returned)
//
// Mirrors providercredentials.go's own identical posture exactly: a
// secret's plaintext value is accepted on create/update
// (CreateSandboxSecretRequest.value / UpdateSandboxSecretRequest.value)
// and immediately encrypted via platform.EncryptToken, but NEVER appears
// in any response this file returns -- sandboxSecretToDTO has no field
// for it at all, only the fixed, non-secret maskedSandboxSecretValue
// placeholder. Never logged either.
//
// # Cross-scope row confusion (IDOR-shaped risk, closed proactively)
//
// Identical risk and identical fix to providercredentials.go's own
// "Cross-scope row confusion" section: a sandbox_secrets row's own id is
// a single, shared UUID namespace across all 3 scopes.
// getSandboxSecretInScope (below) re-fetches and confirms the row's own
// persisted Scope/ScopeTargetID match what THIS route group implies
// before any PUT/DELETE acts on it -- a mismatch is treated identically
// to "no such row", a plain 404.

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/domain/authz"
	"github.com/khazaddev/narvi/internal/domain/sandboxsecret"
	"github.com/khazaddev/narvi/internal/platform"
)

// maskedSandboxSecretValue is the fixed, non-secret placeholder every
// SandboxSecret DTO reports in place of its real value -- mirrors
// maskedProviderCredentialValue exactly.
const maskedSandboxSecretValue = "••••••••"

// parseSandboxSecretID parses chi's own "secretID" URL path param as a
// UUID -- mirrors parseProviderCredentialID's own identical shape.
func parseSandboxSecretID(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	raw := chi.URLParam(r, "secretID")
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil {
		writeError(w, http.StatusBadRequest, "malformed sandbox secret id")
		return pgtype.UUID{}, false
	}
	return id, true
}

// sandboxSecretToDTO converts a stored sqlcgen.SandboxSecret row into its
// REST wire shape -- see this file's own top doc comment for why the real
// value never appears here.
func sandboxSecretToDTO(row sqlcgen.SandboxSecret) restdtos.SandboxSecret {
	return restdtos.SandboxSecret{
		Id:          row.ID.String(),
		Scope:       restdtos.SandboxSecretScope(row.Scope),
		ScopeTarget: restdtos.SandboxSecretScopeTarget(row.ScopeTargetID),
		Name:        row.Name,
		MaskedValue: maskedSandboxSecretValue,
		CreatedAt:   row.CreatedAt.Time,
		UpdatedAt:   row.UpdatedAt.Time,
	}
}

// getSandboxSecretInScope fetches id's own row and confirms it actually
// belongs to (scope, scopeTargetID) -- see this file's own top doc
// comment ("Cross-scope row confusion") for why this check exists at
// all. Returns pgx.ErrNoRows (unwrapped, indistinguishable from a
// genuinely nonexistent id) on either a missing row OR a scope/target
// mismatch. Mirrors getProviderCredentialInScope exactly, reusing this
// package's own shared scopeTargetIDEqual helper.
func getSandboxSecretInScope(ctx context.Context, store *postgres.SandboxSecretStore, id pgtype.UUID, scope sqlcgen.SandboxSecretScope, scopeTargetID *string) (sqlcgen.SandboxSecret, error) {
	row, err := store.Get(ctx, id)
	if err != nil {
		return sqlcgen.SandboxSecret{}, err
	}
	if row.Scope != scope || !scopeTargetIDEqual(row.ScopeTargetID, scopeTargetID) {
		return sqlcgen.SandboxSecret{}, pgx.ErrNoRows
	}
	return row, nil
}

// createSandboxSecret is the shared core POST handler body -- scope/
// action are fixed, and resolveScope is chosen, per calling route group
// (see this file's own exported Create*SandboxSecret wrappers below).
// Reuses this package's OWN scopeTargetResolver type and its 3
// implementations (repoScopeTarget/environmentScopeTarget/
// globalScopeTarget, providercredentials.go) unchanged -- those resolvers
// only ever produce a *string, entirely independent of which secret-
// storage table a caller ultimately uses it for.
func createSandboxSecret(
	w http.ResponseWriter, r *http.Request,
	store *postgres.SandboxSecretStore, tokenEncryptionKey []byte,
	action authz.Action, scope sqlcgen.SandboxSecretScope, resolveScope scopeTargetResolver,
) {
	if !authorize(w, r, action, authz.Resource{}) {
		return
	}
	scopeTargetID, ok := resolveScope(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	logger := platform.Logger(ctx)

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	var req restdtos.CreateSandboxSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request body")
		return
	}

	if err := sandboxsecret.ValidateName(req.Name); err != nil {
		// Never echoes anything beyond the (already caller-supplied,
		// already-known-to-the-caller) name itself -- err's own message
		// includes req.Name, never req.Value.
		writeError(w, http.StatusBadRequest, "invalid secret name: "+err.Error())
		return
	}

	if containsNULByte(req.Value) {
		// Never echoes req.Value -- mirrors createProviderCredential's own
		// identical check exactly.
		writeError(w, http.StatusBadRequest, "secret value must not contain a NUL byte")
		return
	}

	encrypted, err := platform.EncryptToken(tokenEncryptionKey, []byte(req.Value))
	if err != nil {
		logger.Error("httpapi: encrypt sandbox secret value failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	created, err := store.Create(ctx, scope, scopeTargetID, req.Name, encrypted)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a secret with this name already exists at this scope -- update it instead")
			return
		}
		logger.Error("httpapi: create sandbox secret failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, sandboxSecretToDTO(created))
}

// listSandboxSecrets is the shared core GET (list) handler body.
func listSandboxSecrets(
	w http.ResponseWriter, r *http.Request,
	store *postgres.SandboxSecretStore,
	action authz.Action, scope sqlcgen.SandboxSecretScope, resolveScope scopeTargetResolver,
) {
	if !authorize(w, r, action, authz.Resource{}) {
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
		logger.Error("httpapi: list sandbox secrets failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	dtos := make([]restdtos.SandboxSecret, 0, len(rows))
	for _, row := range rows {
		dtos = append(dtos, sandboxSecretToDTO(row))
	}
	writeJSON(w, http.StatusOK, restdtos.ListSandboxSecretsResponse{SandboxSecrets: dtos})
}

// updateSandboxSecretValue is the shared core PUT (rotate) handler body.
func updateSandboxSecretValue(
	w http.ResponseWriter, r *http.Request,
	store *postgres.SandboxSecretStore, tokenEncryptionKey []byte,
	action authz.Action, scope sqlcgen.SandboxSecretScope, resolveScope scopeTargetResolver,
) {
	if !authorize(w, r, action, authz.Resource{}) {
		return
	}
	scopeTargetID, scopeOK := resolveScope(w, r)
	if !scopeOK {
		return
	}
	ctx := r.Context()
	logger := platform.Logger(ctx)

	id, ok := parseSandboxSecretID(w, r)
	if !ok {
		return
	}

	if _, err := getSandboxSecretInScope(ctx, store, id, scope, scopeTargetID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "sandbox secret not found")
			return
		}
		logger.Error("httpapi: get sandbox secret for update failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	var req restdtos.UpdateSandboxSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request body")
		return
	}

	if containsNULByte(req.Value) {
		writeError(w, http.StatusBadRequest, "secret value must not contain a NUL byte")
		return
	}

	encrypted, err := platform.EncryptToken(tokenEncryptionKey, []byte(req.Value))
	if err != nil {
		logger.Error("httpapi: encrypt sandbox secret value failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	updated, err := store.UpdateValue(ctx, id, encrypted)
	if err != nil {
		logger.Error("httpapi: update sandbox secret failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, sandboxSecretToDTO(updated))
}

// deleteSandboxSecret is the shared core DELETE handler body.
func deleteSandboxSecret(
	w http.ResponseWriter, r *http.Request,
	store *postgres.SandboxSecretStore,
	action authz.Action, scope sqlcgen.SandboxSecretScope, resolveScope scopeTargetResolver,
) {
	if !authorize(w, r, action, authz.Resource{}) {
		return
	}
	scopeTargetID, scopeOK := resolveScope(w, r)
	if !scopeOK {
		return
	}
	ctx := r.Context()
	logger := platform.Logger(ctx)

	id, ok := parseSandboxSecretID(w, r)
	if !ok {
		return
	}

	if _, err := getSandboxSecretInScope(ctx, store, id, scope, scopeTargetID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "sandbox secret not found")
			return
		}
		logger.Error("httpapi: get sandbox secret for delete failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if _, err := store.Delete(ctx, id); err != nil {
		logger.Error("httpapi: delete sandbox secret failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Repo-scoped route group: /api/repos/{owner}/{repo}/sandbox-secrets ---

// CreateRepoSandboxSecret backs POST /api/repos/{owner}/{repo}/
// sandbox-secrets -- see this file's own top doc comment for the full
// route table and RBAC rationale.
func CreateRepoSandboxSecret(store *postgres.SandboxSecretStore, tokenEncryptionKey []byte, prSessions *postgres.GitHubPRSessionStore) http.HandlerFunc {
	resolveScope := repoScopeTarget(prSessions)
	return func(w http.ResponseWriter, r *http.Request) {
		createSandboxSecret(w, r, store, tokenEncryptionKey, authz.ActionManageRepoSecrets, sqlcgen.SandboxSecretScopeRepo, resolveScope)
	}
}

// ListRepoSandboxSecrets backs GET /api/repos/{owner}/{repo}/sandbox-secrets.
func ListRepoSandboxSecrets(store *postgres.SandboxSecretStore, prSessions *postgres.GitHubPRSessionStore) http.HandlerFunc {
	resolveScope := repoScopeTarget(prSessions)
	return func(w http.ResponseWriter, r *http.Request) {
		listSandboxSecrets(w, r, store, authz.ActionManageRepoSecrets, sqlcgen.SandboxSecretScopeRepo, resolveScope)
	}
}

// UpdateRepoSandboxSecretValue backs PUT /api/repos/{owner}/{repo}/
// sandbox-secrets/{secretID} -- rotates the encrypted value only.
func UpdateRepoSandboxSecretValue(store *postgres.SandboxSecretStore, tokenEncryptionKey []byte, prSessions *postgres.GitHubPRSessionStore) http.HandlerFunc {
	resolveScope := repoScopeTarget(prSessions)
	return func(w http.ResponseWriter, r *http.Request) {
		updateSandboxSecretValue(w, r, store, tokenEncryptionKey, authz.ActionManageRepoSecrets, sqlcgen.SandboxSecretScopeRepo, resolveScope)
	}
}

// DeleteRepoSandboxSecret backs DELETE /api/repos/{owner}/{repo}/
// sandbox-secrets/{secretID}.
func DeleteRepoSandboxSecret(store *postgres.SandboxSecretStore, prSessions *postgres.GitHubPRSessionStore) http.HandlerFunc {
	resolveScope := repoScopeTarget(prSessions)
	return func(w http.ResponseWriter, r *http.Request) {
		deleteSandboxSecret(w, r, store, authz.ActionManageRepoSecrets, sqlcgen.SandboxSecretScopeRepo, resolveScope)
	}
}

// --- Environment-scoped route group: /api/environments/{environmentID}/sandbox-secrets ---

// CreateEnvironmentSandboxSecret backs POST /api/environments/
// {environmentID}/sandbox-secrets.
func CreateEnvironmentSandboxSecret(store *postgres.SandboxSecretStore, tokenEncryptionKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		createSandboxSecret(w, r, store, tokenEncryptionKey, authz.ActionManageEnvSecrets, sqlcgen.SandboxSecretScopeEnvironment, environmentScopeTarget)
	}
}

// ListEnvironmentSandboxSecrets backs GET /api/environments/
// {environmentID}/sandbox-secrets.
func ListEnvironmentSandboxSecrets(store *postgres.SandboxSecretStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		listSandboxSecrets(w, r, store, authz.ActionManageEnvSecrets, sqlcgen.SandboxSecretScopeEnvironment, environmentScopeTarget)
	}
}

// UpdateEnvironmentSandboxSecretValue backs PUT /api/environments/
// {environmentID}/sandbox-secrets/{secretID} -- rotates the encrypted
// value only.
func UpdateEnvironmentSandboxSecretValue(store *postgres.SandboxSecretStore, tokenEncryptionKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		updateSandboxSecretValue(w, r, store, tokenEncryptionKey, authz.ActionManageEnvSecrets, sqlcgen.SandboxSecretScopeEnvironment, environmentScopeTarget)
	}
}

// DeleteEnvironmentSandboxSecret backs DELETE /api/environments/
// {environmentID}/sandbox-secrets/{secretID}.
func DeleteEnvironmentSandboxSecret(store *postgres.SandboxSecretStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		deleteSandboxSecret(w, r, store, authz.ActionManageEnvSecrets, sqlcgen.SandboxSecretScopeEnvironment, environmentScopeTarget)
	}
}

// --- Global-scoped route group: /api/sandbox-secrets ---
//
// scopeTargetID is always nil here -- there is no URL segment to derive it
// from, matching sandbox_secrets' own CHECK constraint (scope=global
// requires scope_target_id NULL).

// CreateGlobalSandboxSecret backs POST /api/sandbox-secrets.
func CreateGlobalSandboxSecret(store *postgres.SandboxSecretStore, tokenEncryptionKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		createSandboxSecret(w, r, store, tokenEncryptionKey, authz.ActionManageGlobalSecrets, sqlcgen.SandboxSecretScopeGlobal, globalScopeTarget)
	}
}

// ListGlobalSandboxSecrets backs GET /api/sandbox-secrets.
func ListGlobalSandboxSecrets(store *postgres.SandboxSecretStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		listSandboxSecrets(w, r, store, authz.ActionManageGlobalSecrets, sqlcgen.SandboxSecretScopeGlobal, globalScopeTarget)
	}
}

// UpdateGlobalSandboxSecretValue backs PUT /api/sandbox-secrets/{secretID}
// -- rotates the encrypted value only.
func UpdateGlobalSandboxSecretValue(store *postgres.SandboxSecretStore, tokenEncryptionKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		updateSandboxSecretValue(w, r, store, tokenEncryptionKey, authz.ActionManageGlobalSecrets, sqlcgen.SandboxSecretScopeGlobal, globalScopeTarget)
	}
}

// DeleteGlobalSandboxSecret backs DELETE /api/sandbox-secrets/{secretID}.
func DeleteGlobalSandboxSecret(store *postgres.SandboxSecretStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		deleteSandboxSecret(w, r, store, authz.ActionManageGlobalSecrets, sqlcgen.SandboxSecretScopeGlobal, globalScopeTarget)
	}
}
