package reviewverdict

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/domain/autoapproval"
	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/platform"
)

// LoadEligibilityConfig resolves repoFullName's own §21.2 eligibility
// config -- a MISSING repo_settings row, a genuine read ERROR, or a NULL
// column all resolve to autoapproval.DefaultEligibilityConfig's own
// values (fail-CONSERVATIVE: this package's own established narrow
// defaults, never an accidentally-unbounded threshold or empty sensitive
// list) -- mirrors reviewverdict.go's own identical "a missing row or
// read error defaults to false/safe" precedent for blockOnHighRisk/
// sentinelAutofixEnabled. Never returns an error: a degraded read here is
// a POLICY nuance (which threshold applies), never a precondition for
// the eligibility engine to run at all.
func LoadEligibilityConfig(ctx context.Context, deps Deps, repoFullName string) autoapproval.EligibilityConfig {
	cfg := autoapproval.DefaultEligibilityConfig()

	settings, err := deps.RepoSettings.Get(ctx, repoFullName)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			platform.Logger(ctx).Warn("reviewverdict: load eligibility config: read repo settings failed, using defaults", "error", err, "repo_full_name", repoFullName)
		}
		return cfg
	}

	if settings.MaxAutoApproveFilesChanged != nil {
		cfg.MaxFilesChanged = int(*settings.MaxAutoApproveFilesChanged)
	}
	if tags := unmarshalTags(settings.SensitiveBlastRadiusTags); len(tags) > 0 {
		cfg.SensitiveTags = tags
	}
	return cfg
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

// UpsertAutoApprovalSettings idempotently creates-or-updates repoFullName's
// §21.2 settings row -- see UpsertAutoApprovalSettings' own generated doc
// comment (postgres.RepoSettingsStore) for the "touches only these three
// columns" precedent this call relies on.
func UpsertAutoApprovalSettings(ctx context.Context, deps Deps, repoFullName string, settings AutoApprovalSettings) (AutoApprovalSettings, error) {
	var maxFiles *int32
	if settings.MaxAutoApproveFilesChanged != nil {
		v := int32(*settings.MaxAutoApproveFilesChanged)
		maxFiles = &v
	}
	tagsJSON, err := marshalTags(settings.SensitiveBlastRadiusTags)
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
	if len(settings.SensitiveBlastRadiusTags) == 0 {
		tagsJSON = nil
	}

	row, err := deps.RepoSettings.UpsertAutoApprovalSettings(ctx, repoFullName, settings.AutoMergeEnabled, maxFiles, tagsJSON)
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
