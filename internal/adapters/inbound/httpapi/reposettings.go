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
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	appreviewverdict "github.com/khazaddev/narvi/internal/app/reviewverdict"
	"github.com/khazaddev/narvi/internal/domain/authz"
	"github.com/khazaddev/narvi/internal/domain/review"
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
// authorizeAny is helpers.go's own authorize, generalized to "allowed if
// ANY one of actions succeeds" -- Step 62's own first need for this shape
// (GetRepoSettings below): a maintainer who can configure auto-approval
// eligibility (authz.ActionConfigureAutoApprove, §13.3 row 5) must still
// be able to READ this repo's own settings, even though the admin-only
// authz.ActionConfigureBlockOnHighRisk gate alone would otherwise exclude
// them entirely. Unlike authorize (helpers.go), which writes the 403/500
// response itself on the FIRST failure, this checks every action via the
// same pure authz.Authorize directly (no response side effect) and only
// writes a response once ALL of them have been tried -- a single
// authorize() call at read time is only ever the RIGHT shape when
// exactly one action legitimately governs an endpoint, which is no
// longer true here.
func authorizeAny(w http.ResponseWriter, r *http.Request, resource authz.Resource, actions ...authz.Action) bool {
	ctx := r.Context()
	logger := platform.Logger(ctx)

	authUser, ok := platform.UserFromContext(ctx)
	if !ok {
		logger.Error("httpapi: no authenticated user in context (route not mounted behind auth.Middleware?)")
		writeError(w, http.StatusInternalServerError, "internal error")
		return false
	}
	actor := authz.Actor{UserID: authUser.ID, Role: authz.Role(authUser.Role)}

	for _, action := range actions {
		if err := authz.Authorize(actor, action, resource); err == nil {
			return true
		} else if !errors.Is(err, authz.ErrForbidden) {
			logger.Error("httpapi: authz.Authorize failed", "error", err, "action", string(action))
			writeError(w, http.StatusInternalServerError, "internal error")
			return false
		}
	}
	writeError(w, http.StatusForbidden, "not authorized to perform this action")
	return false
}

// GetRepoSettings backs GET /api/repos/{owner}/{repo}/settings -- see this
// file's own doc comment for the base blockOnHighRisk/sentinelAutofixEnabled
// behavior, unchanged by this addition. Step 62 (§21.1/§21.2) extends the
// response with the auto-approval eligibility config, the auto-merge
// toggle, and the contradiction-rate calibration read model -- all THREE
// additive, read-only from this endpoint's own perspective (writes are
// PutAutoApprovalSettings/PutAutoMergeToggle below, each its own
// separately-gated endpoint).
func GetRepoSettings(repoSettings *postgres.RepoSettingsStore, reviewVerdictDeps appreviewverdict.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		// Step 62 (§21.2): a maintainer authorized ONLY for
		// ActionConfigureAutoApprove (row 5) must still be able to read
		// this repo's own settings -- not just the admin-only row 6
		// actions this endpoint originally gated on alone (see
		// authorizeAny's own doc comment above). Step 65 (§24.5) adds
		// ActionToggleAutoRetriggerReview to this SAME "any one of these
		// suffices to read" list -- the same admin-only row as
		// ActionToggleAutoMerge, so this changes nothing about who could
		// already read this endpoint, only documents the new toggle's own
		// read gate explicitly.
		if !authorizeAny(w, r, authz.Resource{}, authz.ActionConfigureBlockOnHighRisk, authz.ActionConfigureAutoApprove, authz.ActionToggleAutoMerge, authz.ActionToggleAutoRetriggerReview) {
			return
		}

		repoFullName, ok := repoFullNameFromRoute(r)
		if !ok {
			writeError(w, http.StatusNotFound, "repo not found")
			return
		}

		resp := restdtos.RepoSettings{RepoFullName: repoFullName}

		settings, err := repoSettings.Get(ctx, repoFullName)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				logger.Error("httpapi: get repo settings failed", "error", err)
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			// Missing row -- every flag defaults to its own safe value
			// (this table's own established precedent), resp already
			// carries the zero-value false/nil defaults for every field.
		} else {
			resp.BlockOnHighRisk = settings.BlockOnHighRisk
			resp.SentinelAutofixEnabled = settings.SentinelAutofixEnabled
			resp.AutoMergeEnabled = settings.AutoMergeEnabled
			resp.AutoRetriggerReviewEnabled = settings.AutoRetriggerReviewEnabled
			if settings.MaxAutoApproveFilesChanged != nil {
				v := int(*settings.MaxAutoApproveFilesChanged)
				resp.MaxAutoApproveFilesChanged = &v
			}
			if tags := autoApprovalTagsFromJSON(settings.SensitiveBlastRadiusTags); tags != nil {
				resp.SensitiveBlastRadiusTags = &tags
			}
		}

		rate, _, sampleSize, computed, err := appreviewverdict.ContradictionRate(ctx, reviewVerdictDeps, repoFullName, time.Now())
		if err != nil {
			logger.Error("httpapi: compute contradiction rate failed", "error", err)
			// A degraded calibration read must never block viewing the
			// rest of this repo's own settings -- resp.ContradictionRateComputed
			// simply stays false (its own zero value), the SAME "not yet
			// computed" rendering a genuine lack of data produces.
		} else if computed {
			resp.ContradictionRateComputed = true
			percent := rate * 100
			resp.ContradictionRatePercent = &percent
			resp.ContradictionSampleSize = sampleSize
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

// autoApprovalTagsFromJSON converts repo_settings.sensitive_blast_radius_tags'
// own raw JSONB bytes into the wire enum slice -- nil input (column NULL)
// or a decode failure both yield a nil result, rendering as the SAME
// "not configured, using the engine's own default" wire value.
func autoApprovalTagsFromJSON(raw []byte) restdtos.RepoSettingsSensitiveBlastRadiusTags {
	tags := reviewTagsFromJSON(raw)
	if len(tags) == 0 {
		return nil
	}
	out := make(restdtos.RepoSettingsSensitiveBlastRadiusTags, len(tags))
	for i, t := range tags {
		out[i] = restdtos.RepoSettingsSensitiveBlastRadiusTagsElem(t)
	}
	return out
}

// reviewTagsFromJSON decodes a JSON array of tag strings (the SAME wire
// shape internal/app/reviewverdict's own unexported unmarshalTags uses --
// duplicated here, one small function, rather than exporting that
// package's own internal conversion helper purely for this one call
// site).
func reviewTagsFromJSON(raw []byte) []review.Tag {
	if len(raw) == 0 {
		return nil
	}
	var strs []string
	if err := json.Unmarshal(raw, &strs); err != nil {
		return nil
	}
	tags := make([]review.Tag, len(strs))
	for i, s := range strs {
		tags[i] = review.Tag(s)
	}
	return tags
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

// PutAutoApprovalSettings backs PUT /api/repos/{owner}/{repo}/auto-approval-settings
// (Step 62, §21.2 stage 1) -- the auto-approval eligibility engine's own
// two per-repo-tunable criteria. Gated SOLELY by authz.ActionConfigureAutoApprove
// (maintainer+, §13.3 row 5) -- a SEPARATE endpoint from PutRepoSettings
// above specifically so a maintainer authorized for this row never needs
// (and never gets asked to hold) admin-only ActionConfigureBlockOnHighRisk/
// ActionToggleSentinelAutoFix/ActionToggleAutoMerge just to reach it (see
// UpdateAutoApprovalSettingsRequest's own doc comment, contracts/rest/v1/
// dtos.schema.json).
//
// §62 review finding C5 (MEDIUM but a privilege boundary, fixed):
// COLUMN-SCOPED write, via appreviewverdict.UpsertAutoApprovalEligibility
// -- touches ONLY max_auto_approve_files_changed/sensitive_blast_radius_tags
// at the SQL level, never repo_settings.auto_merge_enabled (PutAutoMergeToggle
// below owns that column, under its own admin-only gate). Replaces the
// PREVIOUS read-modify-write (read auto_merge_enabled first, pass it
// straight through unchanged on every write) -- that pattern is exactly
// the race this fix closes: a maintainer's write here and an admin's
// PutAutoMergeToggle write, landing concurrently, could each read the
// OTHER's stale pre-write value and silently clobber it, including
// reverting a toggle an admin just armed/disarmed. No read-before-write
// is needed anymore at all -- the column-scoped UPDATE simply never
// touches the column it doesn't own, so there is nothing to preserve.
func PutAutoApprovalSettings(repoSettings *postgres.RepoSettingsStore, reviewVerdictDeps appreviewverdict.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		if !authorize(w, r, authz.ActionConfigureAutoApprove, authz.Resource{}) {
			return
		}

		repoFullName, ok := repoFullNameFromRoute(r)
		if !ok {
			writeError(w, http.StatusNotFound, "repo not found")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		var req restdtos.UpdateAutoApprovalSettingsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed request body")
			return
		}

		var tags []review.Tag
		if req.SensitiveBlastRadiusTags != nil {
			tags = make([]review.Tag, len(*req.SensitiveBlastRadiusTags))
			for i, t := range *req.SensitiveBlastRadiusTags {
				tags[i] = review.Tag(t)
			}
		}

		updated, err := appreviewverdict.UpsertAutoApprovalEligibility(ctx, reviewVerdictDeps, repoFullName, (*int)(req.MaxAutoApproveFilesChanged), tags)
		if err != nil {
			logger.Error("httpapi: upsert auto-approval eligibility failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusOK, autoApprovalSettingsToRepoSettingsDTO(ctx, repoSettings, repoFullName, updated))
	}
}

// PutAutoMergeToggle backs PUT /api/repos/{owner}/{repo}/auto-merge (Step
// 62, §21.2 stage 2) -- arms/disarms the per-repo unattended-merge
// toggle. Gated SOLELY by authz.ActionToggleAutoMerge (admin only, §13.3
// row 6) -- see UpdateAutoMergeToggleRequest's own doc comment for why
// this is a separate endpoint from PutAutoApprovalSettings above.
//
// §62 review finding C5's own fix, mirrored in the other direction:
// COLUMN-SCOPED write, via appreviewverdict.UpsertAutoMergeToggle --
// touches ONLY auto_merge_enabled, never the eligibility-config columns
// PutAutoApprovalSettings above owns. See that handler's own doc comment
// for the full "why" this replaces the previous read-modify-write.
func PutAutoMergeToggle(repoSettings *postgres.RepoSettingsStore, reviewVerdictDeps appreviewverdict.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		if !authorize(w, r, authz.ActionToggleAutoMerge, authz.Resource{}) {
			return
		}

		repoFullName, ok := repoFullNameFromRoute(r)
		if !ok {
			writeError(w, http.StatusNotFound, "repo not found")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		var req restdtos.UpdateAutoMergeToggleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed request body")
			return
		}

		updated, err := appreviewverdict.UpsertAutoMergeToggle(ctx, reviewVerdictDeps, repoFullName, req.Enabled)
		if err != nil {
			logger.Error("httpapi: upsert auto-merge toggle failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusOK, autoApprovalSettingsToRepoSettingsDTO(ctx, repoSettings, repoFullName, updated))
	}
}

// PutAutoRetriggerReviewToggle backs PUT
// /api/repos/{owner}/{repo}/auto-retrigger-review (Step 65, §24.5) --
// arms/disarms the per-repo automatic-re-review-on-new-commits opt-in.
// Gated SOLELY by authz.ActionToggleAutoRetriggerReview (admin only,
// §13.3 row 6) -- see UpdateAutoRetriggerReviewToggleRequest's own doc
// comment for why this is a separate endpoint from PutRepoSettings above,
// mirroring PutAutoMergeToggle's own identical reasoning.
//
// COLUMN-SCOPED write, via postgres.RepoSettingsStore.
// UpsertAutoRetriggerReviewToggle -- touches ONLY
// auto_retrigger_review_enabled, never any other repo_settings column
// (§62 review finding C5's own column-scoped-write discipline, applied
// here from the start rather than as a later fix). Unlike
// PutAutoMergeToggle, this store method already returns the FULL,
// just-written repo_settings row, so no follow-up Get call is needed to
// render every OTHER field on the response.
func PutAutoRetriggerReviewToggle(repoSettings *postgres.RepoSettingsStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		if !authorize(w, r, authz.ActionToggleAutoRetriggerReview, authz.Resource{}) {
			return
		}

		repoFullName, ok := repoFullNameFromRoute(r)
		if !ok {
			writeError(w, http.StatusNotFound, "repo not found")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		var req restdtos.UpdateAutoRetriggerReviewToggleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed request body")
			return
		}

		settings, err := repoSettings.UpsertAutoRetriggerReviewToggle(ctx, repoFullName, req.Enabled)
		if err != nil {
			logger.Error("httpapi: upsert auto-retrigger-review toggle failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		resp := restdtos.RepoSettings{
			RepoFullName:               repoFullName,
			BlockOnHighRisk:            settings.BlockOnHighRisk,
			SentinelAutofixEnabled:     settings.SentinelAutofixEnabled,
			AutoMergeEnabled:           settings.AutoMergeEnabled,
			AutoRetriggerReviewEnabled: settings.AutoRetriggerReviewEnabled,
		}
		if settings.MaxAutoApproveFilesChanged != nil {
			v := int(*settings.MaxAutoApproveFilesChanged)
			resp.MaxAutoApproveFilesChanged = &v
		}
		if tags := autoApprovalTagsFromJSON(settings.SensitiveBlastRadiusTags); tags != nil {
			resp.SensitiveBlastRadiusTags = &tags
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

// autoApprovalSettingsToRepoSettingsDTO renders updated (this call's own
// just-written §21.2 columns) plus a fresh read of blockOnHighRisk/
// sentinelAutofixEnabled (a DIFFERENT write path, PutRepoSettings above)
// into one complete restdtos.RepoSettings -- both PutAutoApprovalSettings
// and PutAutoMergeToggle share this so their own response shape never
// independently drifts from GetRepoSettings' own field-by-field
// construction.
func autoApprovalSettingsToRepoSettingsDTO(ctx context.Context, repoSettings *postgres.RepoSettingsStore, repoFullName string, updated appreviewverdict.AutoApprovalSettings) restdtos.RepoSettings {
	resp := restdtos.RepoSettings{RepoFullName: repoFullName, AutoMergeEnabled: updated.AutoMergeEnabled}
	if updated.MaxAutoApproveFilesChanged != nil {
		v := *updated.MaxAutoApproveFilesChanged
		resp.MaxAutoApproveFilesChanged = &v
	}
	if len(updated.SensitiveBlastRadiusTags) > 0 {
		tags := make(restdtos.RepoSettingsSensitiveBlastRadiusTags, len(updated.SensitiveBlastRadiusTags))
		for i, t := range updated.SensitiveBlastRadiusTags {
			tags[i] = restdtos.RepoSettingsSensitiveBlastRadiusTagsElem(t)
		}
		resp.SensitiveBlastRadiusTags = &tags
	}

	if settings, err := repoSettings.Get(ctx, repoFullName); err == nil {
		resp.BlockOnHighRisk = settings.BlockOnHighRisk
		resp.SentinelAutofixEnabled = settings.SentinelAutofixEnabled
	} else if !errors.Is(err, pgx.ErrNoRows) {
		platform.Logger(ctx).Error("httpapi: read back block-on-high-risk/sentinel-autofix fields failed", "error", err)
	}

	return resp
}
