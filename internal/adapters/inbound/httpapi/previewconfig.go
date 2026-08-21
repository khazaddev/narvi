// This file (previewconfig.go) implements §4.1.2's own amendment
// ("PR preview links at the latest PR commit", exposure amendment):
// GET/PUT /api/repos/{owner}/{repo}/preview-config, backing the three
// rwx_preview_* columns on repo_settings (migrations/
// 000059_repo_settings_rwx_preview.up.sql) -- previously reachable only
// by internal/app/sessionactor/previewpr.go reading the row directly, on
// no REST shape at all.
//
// A DEDICATED endpoint/DTO, deliberately NOT folded into the combined
// GET/PUT /api/repos/{owner}/{repo}/settings (reposettings.go) --
// UpdatePreviewConfigRequest's own doc comment (contracts/rest/v1/
// dtos.schema.json) gives the full "why", but in short: that endpoint
// requires every permission its fields collectively need (its own doc
// comment already gives this as the reason §21's fields were kept off
// it), and a request body carrying a CREDENTIAL (dispatchKey) must not
// share a shape with ordinary configuration. Symmetrically, GET is ALSO
// its own dedicated route here rather than an addition to RepoSettings --
// this is the closer precedent to ProviderCredential/SandboxSecret's own
// GET routes (gated by the SAME single action as their write routes, no
// authorizeAny-style read relaxation) than to RepoSettings' own broader,
// partially-relaxed read gate: a credential-adjacent settings surface,
// not an ordinary policy toggle a maintainer holding some unrelated
// permission should incidentally be able to read.
//
// Both routes gated SOLELY by the NEW admin-only authz.
// ActionConfigurePreviewLinks (§13.3 row 6, internal/domain/authz/
// action.go's own doc comment on it) -- arming this makes every future
// push to this repo trigger a build dispatch on an external provider
// (RWX), unattended, the same reasoning every sibling row-6 automation
// toggle already carries. Mounted behind auth.Middleware, like every
// other browser-facing REST route in this package.
//
// fix/repo-scoped-authorization's own resolveKnownRepo (reposettings.go)
// is reused here unchanged: the URL's own {owner}/{repo} is confirmed
// known to this deployment before any store call runs, exactly like
// every other repo-scoped handler in this package.

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
	"github.com/khazaddev/narvi/internal/platform"
)

// maskedDispatchKeyValue is the fixed, non-secret placeholder every
// configured RWX dispatch key renders as -- copies maskedProviderCredentialValue/
// maskedSandboxSecretValue's own established constant EXACTLY (§4.1.2
// amendment's own explicit instruction): never derived from the real
// key, never varying with its length, proving only that ONE is
// configured.
const maskedDispatchKeyValue = "••••••••"

// previewConfigDTO renders repoFullName + settings' own three
// rwx_preview_* columns into the wire shape, shared by GetPreviewConfig
// and PutPreviewConfig so neither response independently drifts from the
// other -- mirrors reposettings.go's own reviewDepthFieldsFromRow/
// reviewCostBudgetFieldsFromRow "one shared conversion, many call sites"
// precedent. maskedDispatchKey is nil unless settings' own
// RwxPreviewDispatchKey is BOTH non-nil and non-empty -- an empty string
// and a NULL column both mean "not configured" (mirrors
// internal/app/sessionactor/previewpr.go's own readPreviewSettings: "ANY
// ... of the three fields missing/empty collapses to ... false").
func previewConfigDTO(repoFullName string, settings sqlcgen.RepoSetting) restdtos.PreviewConfig {
	resp := restdtos.PreviewConfig{
		RepoFullName:     repoFullName,
		EndpointTemplate: restdtos.PreviewConfigEndpointTemplate(settings.RwxPreviewEndpointTemplate),
		OrgSlug:          restdtos.PreviewConfigOrgSlug(settings.RwxPreviewOrgSlug),
	}
	if settings.RwxPreviewDispatchKey != nil && *settings.RwxPreviewDispatchKey != "" {
		masked := maskedDispatchKeyValue
		resp.MaskedDispatchKey = restdtos.PreviewConfigMaskedDispatchKey(&masked)
	}
	return resp
}

// GetPreviewConfig backs GET /api/repos/{owner}/{repo}/preview-config:
// 403 if the caller fails authz.ActionConfigurePreviewLinks (admin
// only); 404 if {owner}/{repo} is not known to this deployment
// (resolveKnownRepo); 200 with restdtos.PreviewConfig otherwise -- a repo
// with no repo_settings row yet (pgx.ErrNoRows) renders as
// {repoFullName, endpointTemplate: null, orgSlug: null,
// maskedDispatchKey: null}, mirroring GetRepoSettings' own identical
// "no row yet is not an error condition" precedent, never a 404.
func GetPreviewConfig(repoSettings *postgres.RepoSettingsStore, prSessions *postgres.GitHubPRSessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		if !authorize(w, r, authz.ActionConfigurePreviewLinks, authz.Resource{}) {
			return
		}

		repoFullName, ok := resolveKnownRepo(w, r, prSessions)
		if !ok {
			return
		}

		settings, err := repoSettings.Get(ctx, repoFullName)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				logger.Error("httpapi: get repo settings for preview-config failed", "error", err)
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			// Missing row -- every field defaults to null, mirroring
			// GetRepoSettings' own established precedent.
			writeJSON(w, http.StatusOK, restdtos.PreviewConfig{RepoFullName: repoFullName})
			return
		}

		writeJSON(w, http.StatusOK, previewConfigDTO(repoFullName, settings))
	}
}

// PutPreviewConfig backs PUT /api/repos/{owner}/{repo}/preview-config:
// 403 if the caller fails authz.ActionConfigurePreviewLinks (admin
// only); 404 if {owner}/{repo} is not known to this deployment; 400 for
// a malformed request body or a dispatchKey containing an embedded NUL
// byte (Postgres TEXT columns reject one outright -- see
// containsNULByte's own doc comment, providercredentials.go); 200 with
// the resulting restdtos.PreviewConfig otherwise.
//
// endpointTemplate/orgSlug are ALWAYS written to the request's own
// value -- ordinary, full-value semantics (UpdatePreviewConfigRequest's
// own doc comment). dispatchKey gets the ONE partial-write exception on
// this whole surface: req.DispatchKey == nil (the JSON key was absent,
// or explicitly null) leaves the STORED key completely untouched;
// req.DispatchKey pointing at an EMPTY string is the explicit clear
// signal (stored as SQL NULL); any other value rotates it. This is the
// ONE place in this handler that interprets the wire-level "" ->
// clear convention -- postgres.RepoSettingsStore.UpsertPreviewConfig
// itself does no interpretation of its own (its own doc comment), taking
// the already-resolved (dispatchKeyProvided, dispatchKey) pair verbatim.
func PutPreviewConfig(repoSettings *postgres.RepoSettingsStore, prSessions *postgres.GitHubPRSessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		if !authorize(w, r, authz.ActionConfigurePreviewLinks, authz.Resource{}) {
			return
		}

		repoFullName, ok := resolveKnownRepo(w, r, prSessions)
		if !ok {
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		var req restdtos.UpdatePreviewConfigRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed request body")
			return
		}

		// dispatchKeyProvided/dispatchKey: the ONE place this "absent
		// means unchanged, empty string means clear" wire convention is
		// resolved -- see this function's own doc comment.
		var dispatchKeyProvided bool
		var dispatchKey *string
		if raw := (*string)(req.DispatchKey); raw != nil {
			dispatchKeyProvided = true
			if containsNULByte(*raw) {
				writeError(w, http.StatusBadRequest, "dispatchKey must not contain a NUL byte")
				return
			}
			if *raw != "" {
				v := *raw
				dispatchKey = &v
			}
			// *raw == "": dispatchKey stays nil -- the explicit clear
			// signal, stored as SQL NULL.
		}

		settings, err := repoSettings.UpsertPreviewConfig(ctx, repoFullName, req.EndpointTemplate, req.OrgSlug, dispatchKeyProvided, dispatchKey)
		if err != nil {
			logger.Error("httpapi: upsert preview config failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusOK, previewConfigDTO(repoFullName, settings))
	}
}
