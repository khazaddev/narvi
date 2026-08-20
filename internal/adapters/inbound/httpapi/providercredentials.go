// This file (providercredentials.go) implements §25.1's own ("provider
// credential injection", §25.1/§25.3) CP-side MANAGEMENT surface over
// provider_credentials (migrations/000056_provider_credentials.up.sql) --
// this codebase's first generic secret-storage table. Mirrors
// reposettings.go's own "GET/PUT behind auth.Middleware" shape, generalized
// across the 3 RBAC-partitioned scopes §25.3 names, each mounted as its
// own route group (cmd/control-plane/main.go):
//
//   - /api/repos/{owner}/{repo}/provider-credentials        (authz.ActionManageRepoSecrets, admin+maintainer)
//   - /api/environments/{environmentID}/provider-credentials (authz.ActionManageEnvSecrets, admin+maintainer)
//   - /api/provider-credentials                              (authz.ActionManageGlobalSecrets, admin only; scope is always ScopeGlobal)
//
// Each group exposes POST (create) / GET (list) / PUT :credentialID
// (rotate the value) / DELETE :credentialID -- scope and scopeTargetID are
// ALWAYS implied by which route group a request hit (repo_full_name from
// the URL, the environment id from the URL, or nothing at all for
// global), never accepted as a body field: a caller cannot make its own
// URL and body disagree about scope, because the body never says.
//
// No single-row GET by id: List already returns every row at one (scope,
// scopeTarget) pair, at most 3 (one per Provider) -- a dedicated GET
// endpoint would be duplicate surface for no real gain, so it is
// deliberately left out (mirrors the automations engine's own Step 51
// "don't over-build" precedent for a comparably narrow surface).
//
// # Write-only credential value (never returned)
//
// A credential's plaintext value is accepted on create/update
// (CreateProviderCredentialRequest.value / UpdateProviderCredentialRequest.
// value) and immediately encrypted via platform.EncryptToken (the SAME
// AES-256-GCM mechanism identities.access_token_encrypted already uses) --
// but NEVER appears in any response this file returns, at any point:
// providerCredentialToDTO has no field for it at all, only a fixed,
// non-secret maskedProviderCredentialValue placeholder proving a value is
// configured. The plaintext value is also never logged -- matching
// tokenencrypt.go's own "never logs plaintext, key, or ciphertext"
// discipline exactly (grepped for in this Step's own diff before
// reporting done).
//
// # Cross-scope row confusion (IDOR-shaped risk, closed proactively)
//
// A provider_credentials row's own id is a single, shared UUID namespace
// across all 3 scopes -- nothing stops a maintainer who only holds
// ActionManageRepoSecrets from GUESSING or otherwise learning a GLOBAL
// credential's id and hitting the repo-scoped PUT/DELETE route with it,
// which would only check ActionManageRepoSecrets (a check that maintainer
// already passes) rather than the admin-only ActionManageGlobalSecrets the
// TARGET row actually requires. getProviderCredentialInScope (below)
// closes this: every PUT/DELETE re-fetches the row and confirms its own
// persisted Scope/ScopeTargetID match what THIS route group implies before
// acting on it -- a mismatch (including "row belongs to a different repo/
// environment than this URL names") is treated identically to "no such
// row", a plain 404, never a distinguishing error that would confirm the
// id's existence in a different scope.

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/domain/authz"
	"github.com/khazaddev/narvi/internal/platform"
)

// maskedProviderCredentialValue is the fixed, non-secret placeholder every
// ProviderCredential DTO reports in place of its real value -- never
// derived from the real value in any way, so it carries zero information
// about it beyond "a value is configured" (which a row's mere existence
// already implies).
const maskedProviderCredentialValue = "••••••••"

// containsNULByte reports whether value contains an embedded NUL byte
// (U+0000). Both create and update write sites reject a NUL-bearing value
// BEFORE it ever reaches platform.EncryptToken: a value that makes it to
// encryption/storage would, once resolved, eventually reach
// cmd/sandbox-agent/main.go's own fetchProviderCredentialSpawnEnv, which
// builds cmd.Env entries as name+"="+value -- os/exec rejects ANY env
// entry containing a NUL byte ("exec: environment variable contains NUL")
// before fork, so a NUL-bearing credential would fail sandbox boot on
// every spawn/respawn until the row is rotated with a clean value. This
// deliberately checks ONLY for NUL, not the full range of control
// characters -- NUL is the one byte that actually breaks os/exec; a
// broader rejection risks false positives on legitimate API keys that
// might legitimately contain other, harmless bytes.
func containsNULByte(value string) bool {
	return strings.ContainsRune(value, 0)
}

// parseProviderCredentialID parses chi's own "credentialID" URL path
// param as a UUID -- mirrors parseAutomationID/parseSessionID's own
// identical shape (helpers.go, automations.go).
func parseProviderCredentialID(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	raw := chi.URLParam(r, "credentialID")
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil {
		writeError(w, http.StatusBadRequest, "malformed provider credential id")
		return pgtype.UUID{}, false
	}
	return id, true
}

// environmentIDFromRoute parses chi's own "environmentID" URL path param
// as a UUID and returns its canonical, normalized string form (id.String()
// -- lowercase, hyphenated) -- this codebase has no standalone Environment
// CRUD/reuse-by-id surface yet (migrations/000021_environments.up.sql's
// own doc comment), so there is no environments row to verify existence
// against here; this only validates the SHAPE is a well-formed UUID, NOT
// that the environment is known to this deployment -- unlike
// resolveKnownRepo's own repo-scoped equivalent (reposettings.go), which
// validates shape AND confirms the repo is known (fix/repo-scoped-
// authorization) precisely because a sound, non-self-referential
// "known environment" signal does not exist in this schema the way
// github_pr_sessions does for repos (see that function's own doc comment).
func environmentIDFromRoute(w http.ResponseWriter, r *http.Request) (string, bool) {
	raw := chi.URLParam(r, "environmentID")
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil {
		writeError(w, http.StatusBadRequest, "malformed environment id")
		return "", false
	}
	return id.String(), true
}

// providerCredentialToDTO converts a stored sqlcgen.ProviderCredential row
// into its REST wire shape -- see this file's own top doc comment for why
// the real value never appears here.
func providerCredentialToDTO(row sqlcgen.ProviderCredential) restdtos.ProviderCredential {
	return restdtos.ProviderCredential{
		Id:          row.ID.String(),
		Scope:       restdtos.ProviderCredentialScope(row.Scope),
		ScopeTarget: restdtos.ProviderCredentialScopeTarget(row.ScopeTargetID),
		Provider:    restdtos.ProviderCredentialProvider(row.Provider),
		MaskedValue: maskedProviderCredentialValue,
		CreatedAt:   row.CreatedAt.Time,
		UpdatedAt:   row.UpdatedAt.Time,
	}
}

// scopeTargetIDEqual reports whether a and b name the same scope target --
// both nil (global) counts as equal; exactly one nil never does.
func scopeTargetIDEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// getProviderCredentialInScope fetches id's own row and confirms it
// actually belongs to (scope, scopeTargetID) -- see this file's own top
// doc comment ("Cross-scope row confusion") for why this check exists at
// all. Returns pgx.ErrNoRows (unwrapped, indistinguishable from a
// genuinely nonexistent id) on either a missing row OR a scope/target
// mismatch.
func getProviderCredentialInScope(ctx context.Context, store *postgres.ProviderCredentialStore, id pgtype.UUID, scope sqlcgen.ProviderCredentialScope, scopeTargetID *string) (sqlcgen.ProviderCredential, error) {
	row, err := store.Get(ctx, id)
	if err != nil {
		return sqlcgen.ProviderCredential{}, err
	}
	if row.Scope != scope || !scopeTargetIDEqual(row.ScopeTargetID, scopeTargetID) {
		return sqlcgen.ProviderCredential{}, pgx.ErrNoRows
	}
	return row, nil
}

// scopeTargetResolver resolves THIS request's own scope target -- the
// route's repoFullName (confirmed known to this deployment), the route's
// environment id (shape-checked only), or nil for the global scope. Every
// shared core function below calls this ONLY AFTER authorize() has already
// succeeded, never before -- by construction, since it is the very next
// statement in each function body -- so a role-denied caller never learns
// anything about whether the named repo exists, matching every other
// repo-scoped handler in this package (reposettings.go/reviewanalytics.go/
// falsepositivepatterns.go, which all resolve+validate the route's repo
// strictly after their own role check too).
//
// fix/repo-scoped-authorization: before this batch, each of this file's 4
// exported Create/List/Update/Delete-Repo* wrappers resolved
// {owner}/{repo} itself (via the since-removed repoFullNameFromRoute) and
// passed the raw string straight to the shared core function below, which
// then authorized the caller's ROLE against an empty authz.Resource{} --
// the repo name reached postgres.ProviderCredentialStore having been
// through NO check beyond "is this syntactically owner/repo shaped".
// scopeTargetResolver is this file's OWN answer to the same structural gap
// reposettings.go's resolveKnownRepo doc comment describes: repoScopeTarget
// (below) is now the ONLY path from route params to a repoFullName this
// file's shared core functions will accept, and it always confirms the
// repo is known before returning one.
type scopeTargetResolver func(w http.ResponseWriter, r *http.Request) (*string, bool)

// repoScopeTarget is the scopeTargetResolver for the repo-scoped route
// group -- delegates to reposettings.go's own resolveKnownRepo (the SAME
// function, and SAME github_pr_sessions-backed check, every other
// repo-scoped handler in this package uses; see that function's own doc
// comment for the full "why" this is a sound entitlement signal).
func repoScopeTarget(prSessions *postgres.GitHubPRSessionStore) scopeTargetResolver {
	return func(w http.ResponseWriter, r *http.Request) (*string, bool) {
		repoFullName, ok := resolveKnownRepo(w, r, prSessions)
		if !ok {
			return nil, false
		}
		return &repoFullName, true
	}
}

// environmentScopeTarget is the scopeTargetResolver for the
// environment-scoped route group -- unchanged behavior from before this
// batch: only the SHAPE is validated (a well-formed UUID), never "known to
// this deployment", because no environments row/CRUD/reuse-by-id surface
// exists yet to check against at all (environmentIDFromRoute's own doc
// comment) -- there is nothing sound to check here, unlike the repo case.
func environmentScopeTarget(w http.ResponseWriter, r *http.Request) (*string, bool) {
	id, ok := environmentIDFromRoute(w, r)
	if !ok {
		return nil, false
	}
	return &id, true
}

// globalScopeTarget is the trivial scopeTargetResolver for the global route
// group -- there is no URL segment to resolve at all.
func globalScopeTarget(_ http.ResponseWriter, _ *http.Request) (*string, bool) {
	return nil, true
}

// createProviderCredential is the shared core POST handler body -- scope/
// action are fixed, and resolveScope is chosen, per calling route group
// (see this file's own exported Create*ProviderCredential wrappers below).
func createProviderCredential(
	w http.ResponseWriter, r *http.Request,
	store *postgres.ProviderCredentialStore, tokenEncryptionKey []byte,
	action authz.Action, scope sqlcgen.ProviderCredentialScope, resolveScope scopeTargetResolver,
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
	var req restdtos.CreateProviderCredentialRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Covers a malformed body, an unrecognized provider (restdtos'
		// own generated CreateProviderCredentialRequestProvider.
		// UnmarshalJSON already rejects anything outside
		// google/anthropic/openai), and an empty value (its own generated
		// UnmarshalJSON already enforces minLength 1) -- all one 400, no
		// need for a second, redundant check here.
		writeError(w, http.StatusBadRequest, "malformed request body")
		return
	}

	if containsNULByte(req.Value) {
		// Never echoes req.Value -- see this file's own top doc comment
		// and containsNULByte's own doc comment for why this check exists
		// at all.
		writeError(w, http.StatusBadRequest, "credential value must not contain a NUL byte")
		return
	}

	// Never logs req.Value, at any point, under any circumstance -- see
	// this file's own top doc comment.
	encrypted, err := platform.EncryptToken(tokenEncryptionKey, []byte(req.Value))
	if err != nil {
		logger.Error("httpapi: encrypt provider credential value failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	created, err := store.Create(ctx, scope, scopeTargetID, sqlcgen.ProviderCredentialProvider(req.Provider), encrypted)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a credential for this provider already exists at this scope -- update it instead")
			return
		}
		logger.Error("httpapi: create provider credential failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, providerCredentialToDTO(created))
}

// listProviderCredentials is the shared core GET (list) handler body.
func listProviderCredentials(
	w http.ResponseWriter, r *http.Request,
	store *postgres.ProviderCredentialStore,
	action authz.Action, scope sqlcgen.ProviderCredentialScope, resolveScope scopeTargetResolver,
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
		logger.Error("httpapi: list provider credentials failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	dtos := make([]restdtos.ProviderCredential, 0, len(rows))
	for _, row := range rows {
		dtos = append(dtos, providerCredentialToDTO(row))
	}
	writeJSON(w, http.StatusOK, restdtos.ListProviderCredentialsResponse{ProviderCredentials: dtos})
}

// updateProviderCredentialValue is the shared core PUT (rotate) handler
// body.
func updateProviderCredentialValue(
	w http.ResponseWriter, r *http.Request,
	store *postgres.ProviderCredentialStore, tokenEncryptionKey []byte,
	action authz.Action, scope sqlcgen.ProviderCredentialScope, resolveScope scopeTargetResolver,
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

	id, ok := parseProviderCredentialID(w, r)
	if !ok {
		return
	}

	if _, err := getProviderCredentialInScope(ctx, store, id, scope, scopeTargetID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "provider credential not found")
			return
		}
		logger.Error("httpapi: get provider credential for update failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	var req restdtos.UpdateProviderCredentialRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request body")
		return
	}

	if containsNULByte(req.Value) {
		// Never echoes req.Value -- see this file's own top doc comment
		// and containsNULByte's own doc comment for why this check exists
		// at all.
		writeError(w, http.StatusBadRequest, "credential value must not contain a NUL byte")
		return
	}

	encrypted, err := platform.EncryptToken(tokenEncryptionKey, []byte(req.Value))
	if err != nil {
		logger.Error("httpapi: encrypt provider credential value failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	updated, err := store.UpdateValue(ctx, id, encrypted)
	if err != nil {
		logger.Error("httpapi: update provider credential failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, providerCredentialToDTO(updated))
}

// deleteProviderCredential is the shared core DELETE handler body.
func deleteProviderCredential(
	w http.ResponseWriter, r *http.Request,
	store *postgres.ProviderCredentialStore,
	action authz.Action, scope sqlcgen.ProviderCredentialScope, resolveScope scopeTargetResolver,
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

	id, ok := parseProviderCredentialID(w, r)
	if !ok {
		return
	}

	if _, err := getProviderCredentialInScope(ctx, store, id, scope, scopeTargetID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "provider credential not found")
			return
		}
		logger.Error("httpapi: get provider credential for delete failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if _, err := store.Delete(ctx, id); err != nil {
		logger.Error("httpapi: delete provider credential failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Repo-scoped route group: /api/repos/{owner}/{repo}/provider-credentials ---

// CreateRepoProviderCredential backs POST /api/repos/{owner}/{repo}/
// provider-credentials -- see this file's own top doc comment for the
// full route table and RBAC rationale, and repoScopeTarget's own doc
// comment for the fix/repo-scoped-authorization "known repo" gate this
// wrapper now applies via resolveScope.
func CreateRepoProviderCredential(store *postgres.ProviderCredentialStore, tokenEncryptionKey []byte, prSessions *postgres.GitHubPRSessionStore) http.HandlerFunc {
	resolveScope := repoScopeTarget(prSessions)
	return func(w http.ResponseWriter, r *http.Request) {
		createProviderCredential(w, r, store, tokenEncryptionKey, authz.ActionManageRepoSecrets, sqlcgen.ProviderCredentialScopeRepo, resolveScope)
	}
}

// ListRepoProviderCredentials backs GET /api/repos/{owner}/{repo}/
// provider-credentials.
func ListRepoProviderCredentials(store *postgres.ProviderCredentialStore, prSessions *postgres.GitHubPRSessionStore) http.HandlerFunc {
	resolveScope := repoScopeTarget(prSessions)
	return func(w http.ResponseWriter, r *http.Request) {
		listProviderCredentials(w, r, store, authz.ActionManageRepoSecrets, sqlcgen.ProviderCredentialScopeRepo, resolveScope)
	}
}

// UpdateRepoProviderCredentialValue backs PUT /api/repos/{owner}/{repo}/
// provider-credentials/{credentialID} -- rotates the encrypted value only.
func UpdateRepoProviderCredentialValue(store *postgres.ProviderCredentialStore, tokenEncryptionKey []byte, prSessions *postgres.GitHubPRSessionStore) http.HandlerFunc {
	resolveScope := repoScopeTarget(prSessions)
	return func(w http.ResponseWriter, r *http.Request) {
		updateProviderCredentialValue(w, r, store, tokenEncryptionKey, authz.ActionManageRepoSecrets, sqlcgen.ProviderCredentialScopeRepo, resolveScope)
	}
}

// DeleteRepoProviderCredential backs DELETE /api/repos/{owner}/{repo}/
// provider-credentials/{credentialID}.
func DeleteRepoProviderCredential(store *postgres.ProviderCredentialStore, prSessions *postgres.GitHubPRSessionStore) http.HandlerFunc {
	resolveScope := repoScopeTarget(prSessions)
	return func(w http.ResponseWriter, r *http.Request) {
		deleteProviderCredential(w, r, store, authz.ActionManageRepoSecrets, sqlcgen.ProviderCredentialScopeRepo, resolveScope)
	}
}

// --- Environment-scoped route group: /api/environments/{environmentID}/provider-credentials ---

// CreateEnvironmentProviderCredential backs POST /api/environments/
// {environmentID}/provider-credentials.
func CreateEnvironmentProviderCredential(store *postgres.ProviderCredentialStore, tokenEncryptionKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		createProviderCredential(w, r, store, tokenEncryptionKey, authz.ActionManageEnvSecrets, sqlcgen.ProviderCredentialScopeEnvironment, environmentScopeTarget)
	}
}

// ListEnvironmentProviderCredentials backs GET /api/environments/
// {environmentID}/provider-credentials.
func ListEnvironmentProviderCredentials(store *postgres.ProviderCredentialStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		listProviderCredentials(w, r, store, authz.ActionManageEnvSecrets, sqlcgen.ProviderCredentialScopeEnvironment, environmentScopeTarget)
	}
}

// UpdateEnvironmentProviderCredentialValue backs PUT /api/environments/
// {environmentID}/provider-credentials/{credentialID} -- rotates the
// encrypted value only.
func UpdateEnvironmentProviderCredentialValue(store *postgres.ProviderCredentialStore, tokenEncryptionKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		updateProviderCredentialValue(w, r, store, tokenEncryptionKey, authz.ActionManageEnvSecrets, sqlcgen.ProviderCredentialScopeEnvironment, environmentScopeTarget)
	}
}

// DeleteEnvironmentProviderCredential backs DELETE /api/environments/
// {environmentID}/provider-credentials/{credentialID}.
func DeleteEnvironmentProviderCredential(store *postgres.ProviderCredentialStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		deleteProviderCredential(w, r, store, authz.ActionManageEnvSecrets, sqlcgen.ProviderCredentialScopeEnvironment, environmentScopeTarget)
	}
}

// --- Global-scoped route group: /api/provider-credentials ---
//
// scopeTargetID is always nil here -- there is no URL segment to derive it
// from, matching provider_credentials' own CHECK constraint (scope=global
// requires scope_target_id NULL).

// CreateGlobalProviderCredential backs POST /api/provider-credentials.
func CreateGlobalProviderCredential(store *postgres.ProviderCredentialStore, tokenEncryptionKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		createProviderCredential(w, r, store, tokenEncryptionKey, authz.ActionManageGlobalSecrets, sqlcgen.ProviderCredentialScopeGlobal, globalScopeTarget)
	}
}

// ListGlobalProviderCredentials backs GET /api/provider-credentials.
func ListGlobalProviderCredentials(store *postgres.ProviderCredentialStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		listProviderCredentials(w, r, store, authz.ActionManageGlobalSecrets, sqlcgen.ProviderCredentialScopeGlobal, globalScopeTarget)
	}
}

// UpdateGlobalProviderCredentialValue backs PUT /api/provider-credentials/
// {credentialID} -- rotates the encrypted value only.
func UpdateGlobalProviderCredentialValue(store *postgres.ProviderCredentialStore, tokenEncryptionKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		updateProviderCredentialValue(w, r, store, tokenEncryptionKey, authz.ActionManageGlobalSecrets, sqlcgen.ProviderCredentialScopeGlobal, globalScopeTarget)
	}
}

// DeleteGlobalProviderCredential backs DELETE /api/provider-credentials/
// {credentialID}.
func DeleteGlobalProviderCredential(store *postgres.ProviderCredentialStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		deleteProviderCredential(w, r, store, authz.ActionManageGlobalSecrets, sqlcgen.ProviderCredentialScopeGlobal, globalScopeTarget)
	}
}
