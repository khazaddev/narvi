// This file (reposettings.go) implements Step 47's ("server-side
// verdict", §8.2/§21.2) own admin-facing settings surface: GET/PUT
// /api/repos/{owner}/{repo}/settings, backing repo_settings (migrations/
// 000044_repo_settings.up.sql). Today this holds exactly one flag --
// blockOnHighRisk -- but the table (and this endpoint's own DTO,
// restdtos.RepoSettings) is deliberately shaped to grow further admin,
// per-repo booleans later (§17's sentinel auto-fix toggle, §21's auto-
// merge toggle, §24's automatic-re-review opt-in) without a new table or
// endpoint per toggle.
//
// Both routes are gated by domain/authz.Authorize(actor,
// authz.ActionConfigureBlockOnHighRisk, ...) -- admin only (§13.3 row 6,
// mirroring ActionToggleSentinelAutoFix's own identical placement and
// reasoning: this changes what runs UNATTENDED on a repo's own PRs, up to
// and including a hard REQUEST_CHANGES block, never a per-PR human
// judgment call the way row 5's maintainer-level actions are).
//
// Mounted behind auth.Middleware, alongside every other browser-facing
// REST route in this package (unlike scm-credentials/snapshot-mint/
// review-verdict, which are sandbox-bearer-authenticated: this is an
// ADMIN, not the sandbox agent, configuring a policy flag).

package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/domain/authz"
	"github.com/khazaddev/narvi/internal/platform"
)

// repoFullNameFromRoute joins the {owner}/{repo} chi URL params into the
// same "owner/repo" shape github_pr_sessions.repo_full_name/repo_settings.
// repo_full_name already use -- both chi params are required by the route
// pattern itself, so an empty owner or repo here means a route-mounting
// bug, not malformed caller input; still guarded defensively rather than
// building a bare "/repo" or "owner/" key.
func repoFullNameFromRoute(r *http.Request) (string, bool) {
	owner := chi.URLParam(r, "owner")
	repo := chi.URLParam(r, "repo")
	if owner == "" || repo == "" {
		return "", false
	}
	return owner + "/" + repo, true
}

// GetRepoSettings backs GET /api/repos/{owner}/{repo}/settings: 403 if the
// caller fails authz.ActionConfigureBlockOnHighRisk (admin only); 200 with
// restdtos.RepoSettings otherwise -- a repo with no repo_settings row yet
// (pgx.ErrNoRows) renders as {repoFullName, blockOnHighRisk: false,
// sentinelAutofixEnabled: false}, both documented safe defaults
// (migrations/000044_repo_settings.up.sql, migrations/
// 000048_repo_settings_sentinel_autofix.up.sql), never a 404: "no row
// yet" is not an error condition for a policy flag that always has a
// well-defined value.
func GetRepoSettings(repoSettings *postgres.RepoSettingsStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		if !authorize(w, r, authz.ActionConfigureBlockOnHighRisk, authz.Resource{}) {
			return
		}

		repoFullName, ok := repoFullNameFromRoute(r)
		if !ok {
			writeError(w, http.StatusNotFound, "repo not found")
			return
		}

		settings, err := repoSettings.Get(ctx, repoFullName)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				logger.Error("httpapi: get repo settings failed", "error", err)
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			writeJSON(w, http.StatusOK, restdtos.RepoSettings{RepoFullName: repoFullName, BlockOnHighRisk: false, SentinelAutofixEnabled: false})
			return
		}

		writeJSON(w, http.StatusOK, restdtos.RepoSettings{
			RepoFullName:           repoFullName,
			BlockOnHighRisk:        settings.BlockOnHighRisk,
			SentinelAutofixEnabled: settings.SentinelAutofixEnabled,
		})
	}
}

// PutRepoSettings backs PUT /api/repos/{owner}/{repo}/settings: 403 if the
// caller fails EITHER authz.ActionConfigureBlockOnHighRisk OR (Step 48)
// authz.ActionToggleSentinelAutoFix -- both admin-only today (§13.3 row
// 6), checked independently since this ONE endpoint now writes both
// flags together (repo_settings' own "always the full, current desired
// value, never a partial patch" precedent, migrations/000044's own doc
// comment) -- a future divergence in either action's own role matrix
// would still correctly demand BOTH permissions for this combined write,
// rather than silently falling back to whichever check happens to run
// first; 400 for a malformed request body; 200 with the resulting
// restdtos.RepoSettings otherwise. Idempotent create-or-update
// (postgres.RepoSettingsStore.Upsert).
func PutRepoSettings(repoSettings *postgres.RepoSettingsStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		if !authorize(w, r, authz.ActionConfigureBlockOnHighRisk, authz.Resource{}) {
			return
		}
		if !authorize(w, r, authz.ActionToggleSentinelAutoFix, authz.Resource{}) {
			return
		}

		repoFullName, ok := repoFullNameFromRoute(r)
		if !ok {
			writeError(w, http.StatusNotFound, "repo not found")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		var req restdtos.UpdateRepoSettingsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed request body")
			return
		}

		settings, err := repoSettings.Upsert(ctx, repoFullName, req.BlockOnHighRisk, req.SentinelAutofixEnabled)
		if err != nil {
			logger.Error("httpapi: upsert repo settings failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusOK, restdtos.RepoSettings{
			RepoFullName:           repoFullName,
			BlockOnHighRisk:        settings.BlockOnHighRisk,
			SentinelAutofixEnabled: settings.SentinelAutofixEnabled,
		})
	}
}
