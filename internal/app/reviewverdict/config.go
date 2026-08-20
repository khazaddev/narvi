package reviewverdict

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/domain/autoapproval"
	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/platform"
)

// ErrLoadEligibilityConfigFailed is LoadEligibilityConfig's own sentinel
// for a GENUINE repo_settings read failure (anything other than
// pgx.ErrNoRows) -- Step 62 review finding C3. Before this fix,
// LoadEligibilityConfig had no way to report this at all (it returned a
// bare autoapproval.EligibilityConfig, no error): a transient store error
// silently substituted the engine's own WIDER built-in defaults for a
// repo's deliberately-NARROWER configured threshold/sensitive-tag list,
// inside the one gate that decides an UNATTENDED merge -- widening policy
// on a degraded read is exactly backwards for this call site. Wrapped
// (errors.Is-checkable) so a caller can log/alert distinctly, though every
// real caller today treats it identically to any other infra error at its
// own site: fail CLOSED.
var ErrLoadEligibilityConfigFailed = errors.New("reviewverdict: load eligibility config: repo settings read failed")

// LoadEligibilityConfig resolves repoFullName's own §21.2 eligibility
// config. Three distinct outcomes, deliberately NOT collapsed into one:
//
//   - A MISSING repo_settings row (pgx.ErrNoRows) resolves to
//     autoapproval.DefaultEligibilityConfig's own values, err=nil -- a
//     legitimate, common "never configured yet" state (this package's own
//     established "missing row means every flag defaults to its own safe
//     value" precedent), never an error.
//   - A NULL column on an EXISTING row (settings.MaxAutoApproveFilesChanged
//     nil / SensitiveBlastRadiusTags empty) resolves to that ONE field's
//     own default, err=nil -- the row exists, this repo simply never
//     overrode that one field.
//   - A GENUINE read error (anything else) returns
//     autoapproval.EligibilityConfig{}, ErrLoadEligibilityConfigFailed --
//     Step 62 review finding C3 (BLOCKER, fixed): this function used to
//     silently substitute the engine's own WIDER defaults here, the exact
//     opposite of "cannot establish this repo's policy" fail-closed
//     behavior an unattended-merge gate requires. THE CALLER MUST TREAT A
//     NON-NIL ERROR AS "NOT ELIGIBLE", never fall back to the returned
//     (zero-value, meaningless) config -- see revalidateCore/
//     computeRealEligibility (internal/app/decisioninbox) for the two real
//     callers, each failing closed in the way appropriate to its own
//     context (a hard propagated error for revalidateCore's own action
//     endpoint; a degraded "not eligible" row for computeRealEligibility's
//     own best-effort read-model build) -- see each call site's own doc
//     comment for why the two differ.
func LoadEligibilityConfig(ctx context.Context, deps Deps, repoFullName string) (autoapproval.EligibilityConfig, error) {
	cfg := autoapproval.DefaultEligibilityConfig()

	settings, err := deps.RepoSettings.Get(ctx, repoFullName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return cfg, nil
		}
		platform.Logger(ctx).Error("reviewverdict: load eligibility config: read repo settings failed -- failing CLOSED (caller must treat this as not eligible, never fall back to defaults)", "error", err, "repo_full_name", repoFullName)
		return autoapproval.EligibilityConfig{}, fmt.Errorf("%w: %w", ErrLoadEligibilityConfigFailed, err)
	}

	if settings.MaxAutoApproveFilesChanged != nil {
		cfg.MaxFilesChanged = int(*settings.MaxAutoApproveFilesChanged)
	}
	if tags := unmarshalTags(settings.SensitiveBlastRadiusTags); len(tags) > 0 {
		cfg.SensitiveTags = tags
	}
	return cfg, nil
}

// AutoMergeEnabled reports whether repoFullName's own auto-merge toggle
// (§21.2 stage 2) is currently armed -- a missing row or read error both
// fail CLOSED to false, mirroring every other per-repo policy flag this
// codebase reads (§24.5's own "if the setting cannot be read... treated
// as OFF" precedent) -- an unattended merge is exactly the wrong place
// to fail open on a degraded read.
func AutoMergeEnabled(ctx context.Context, deps Deps, repoFullName string) bool {
	settings, err := deps.RepoSettings.Get(ctx, repoFullName)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			platform.Logger(ctx).Warn("reviewverdict: check auto-merge enabled: read repo settings failed, defaulting to false", "error", err, "repo_full_name", repoFullName)
		}
		return false
	}
	return settings.AutoMergeEnabled
}

// AutoApprovalSettings is the §21.2 config surface's own read/write
// shape -- httpapi/reposettings.go's own GET/PUT handlers convert this
// to/from restdtos.RepoSettings/UpdateRepoSettingsRequest. Unlike
// LoadEligibilityConfig above, this carries the RAW stored values
// (nil/empty means "not configured", never silently substituted with the
// engine's own default) -- a settings UI needs to render the difference
// between "explicitly set to 20" and "unset, currently defaulting to
// 20", which LoadEligibilityConfig's own already-resolved
// autoapproval.EligibilityConfig cannot express.
type AutoApprovalSettings struct {
	AutoMergeEnabled           bool
	MaxAutoApproveFilesChanged *int
	SensitiveBlastRadiusTags   []review.Tag
}

// GetAutoApprovalSettings reads repoFullName's own raw, unresolved §21.2
// settings -- a missing row (pgx.ErrNoRows, unwrapped) means every field
// is at its own unset/default state; the caller (httpapi.GetRepoSettings)
// already has an established "missing row renders as safe defaults, never
// a 404" precedent for this table.
func GetAutoApprovalSettings(ctx context.Context, deps Deps, repoFullName string) (AutoApprovalSettings, error) {
	settings, err := deps.RepoSettings.Get(ctx, repoFullName)
	if err != nil {
		return AutoApprovalSettings{}, err
	}
	out := AutoApprovalSettings{AutoMergeEnabled: settings.AutoMergeEnabled}
	if settings.MaxAutoApproveFilesChanged != nil {
		v := int(*settings.MaxAutoApproveFilesChanged)
		out.MaxAutoApproveFilesChanged = &v
	}
	out.SensitiveBlastRadiusTags = unmarshalTags(settings.SensitiveBlastRadiusTags)
	return out, nil
}

// UpsertAutoMergeToggle idempotently creates-or-updates repoFullName's
// §21.2 stage-2 auto-merge toggle -- Step 62 review finding C5 (MEDIUM but a
// privilege boundary, fixed): column-scoped, touches ONLY
// auto_merge_enabled (postgres.RepoSettingsStore.UpsertAutoMergeToggle's
// own generated-query doc comment) -- see that method's own doc comment
// for the full "why" this replaces the PREVIOUS combined
// UpsertAutoApprovalSettings (which wrote all three §21.2 columns
// together, letting this endpoint's own write and
// UpsertAutoApprovalEligibility's silently race). The returned
// AutoApprovalSettings reflects the row's CURRENT
// MaxAutoApproveFilesChanged/SensitiveBlastRadiusTags too (this query's
// own RETURNING *) -- whatever a prior UpsertAutoApprovalEligibility call
// already set, UNMODIFIED by this call, so a caller building a REST
// response never needs a separate read to render the complete picture.
func UpsertAutoMergeToggle(ctx context.Context, deps Deps, repoFullName string, autoMergeEnabled bool) (AutoApprovalSettings, error) {
	row, err := deps.RepoSettings.UpsertAutoMergeToggle(ctx, repoFullName, autoMergeEnabled)
	if err != nil {
		return AutoApprovalSettings{}, err
	}
	return autoApprovalSettingsFromRow(row), nil
}

// UpsertAutoApprovalEligibility idempotently creates-or-updates
// repoFullName's §21.2 stage-1 eligibility config -- Step 62 review finding
// C5's own column-scoped sibling: touches ONLY
// max_auto_approve_files_changed/sensitive_blast_radius_tags, leaving
// auto_merge_enabled untouched (postgres.RepoSettingsStore.
// UpsertAutoApprovalEligibility's own generated-query doc comment) -- see
// UpsertAutoMergeToggle's own doc comment above for the full "why" this
// replaces the previous combined UpsertAutoApprovalSettings. The returned
// AutoApprovalSettings.AutoMergeEnabled reflects the row's CURRENT value
// too (this query's own RETURNING *), unmodified by this call.
func UpsertAutoApprovalEligibility(ctx context.Context, deps Deps, repoFullName string, maxAutoApproveFilesChanged *int, sensitiveBlastRadiusTags []review.Tag) (AutoApprovalSettings, error) {
	var maxFiles *int32
	if maxAutoApproveFilesChanged != nil {
		v := int32(*maxAutoApproveFilesChanged)
		maxFiles = &v
	}
	tagsJSON, err := marshalTags(sensitiveBlastRadiusTags)
	if err != nil {
		return AutoApprovalSettings{}, err
	}
	// An explicitly-empty tag list is stored as NULL (never "[]"), so
	// LoadEligibilityConfig's own "len(tags) > 0" check (config.go)
	// correctly falls back to the engine's own default list rather than
	// reading an empty configured list as "this repo has zero sensitive
	// tags, deliberately" -- §21.2 names the default list as a STARTING
	// point every repo begins with, and this schema has no separate way
	// to represent "explicitly cleared to empty" from "never configured"
	// -- collapsing the two is a deliberate simplification, not an
	// oversight: a repo that genuinely wants zero sensitive tags can
	// still get arbitrarily close by naming a list of tags known never
	// to appear, but this Step does not add a THIRD tri-state wire
	// representation for a distinction nothing else in §21.2 asks for.
	if len(sensitiveBlastRadiusTags) == 0 {
		tagsJSON = nil
	}

	row, err := deps.RepoSettings.UpsertAutoApprovalEligibility(ctx, repoFullName, maxFiles, tagsJSON)
	if err != nil {
		return AutoApprovalSettings{}, err
	}
	return autoApprovalSettingsFromRow(row), nil
}

func autoApprovalSettingsFromRow(row sqlcgen.RepoSetting) AutoApprovalSettings {
	out := AutoApprovalSettings{AutoMergeEnabled: row.AutoMergeEnabled}
	if row.MaxAutoApproveFilesChanged != nil {
		v := int(*row.MaxAutoApproveFilesChanged)
		out.MaxAutoApproveFilesChanged = &v
	}
	out.SensitiveBlastRadiusTags = unmarshalTags(row.SensitiveBlastRadiusTags)
	return out
}
