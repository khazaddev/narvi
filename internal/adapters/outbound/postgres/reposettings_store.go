package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// RepoSettingsStore is a thin, pass-through wrapper around the
// sqlc-generated repo_settings queries (§8.2/Step 47, §21.2) -- see
// migrations/000044_repo_settings.up.sql's own doc comment for the "one
// shared table, not one bespoke table per toggle" design this store's own
// narrow surface (today: BlockOnHighRisk alone) is expected to grow
// alongside. No caching, no retries, no business rules -- callers decide
// what a missing row (pgx.ErrNoRows, unwrapped) means for their own
// purposes (internal/adapters/inbound/httpapi/reviewverdict.go treats it,
// and any other read error, as "block_on_high_risk defaults to false",
// mirroring §24.5's own identical per-repo-policy-flag fail-closed
// precedent).
type RepoSettingsStore struct {
	q *sqlcgen.Queries
}

// NewRepoSettingsStore builds a RepoSettingsStore backed by pool.
func NewRepoSettingsStore(pool *pgxpool.Pool) *RepoSettingsStore {
	return &RepoSettingsStore{q: sqlcgen.New(pool)}
}

// WithTx returns a RepoSettingsStore whose queries run on tx instead of the
// pool this store was built with -- mirrors GitHubPRSessionStore.WithTx/
// TurnStore.WithTx exactly (every other multi-column-write store in this
// package already has this). §62 review findings C3/C5 are this store's
// first real callers: C5's column-scoped upserts (PutAutoApprovalSettings/
// PutAutoMergeToggle, httpapi/reposettings.go) don't themselves need a
// shared transaction (each is already a single atomic UPDATE), but this
// package's own established integration-test fault-injection idiom (an
// already-rolled-back tx standing in for a genuine store outage, see
// internal/app/decisioninbox's own TestBuild_CredentialResolutionErrorDegradesRatherThanRenderingNoGitHub)
// needs SOME WithTx to target -- adding it here rather than inventing a
// parallel fault-injection mechanism just for this one store.
func (s *RepoSettingsStore) WithTx(tx pgx.Tx) *RepoSettingsStore {
	return &RepoSettingsStore{q: s.q.WithTx(tx)}
}

// Get fetches repoFullName's own settings row. pgx.ErrNoRows (unwrapped)
// means no row exists yet -- every flag on it defaults to its own safe
// value; this is not an error condition for a caller to alarm on.
func (s *RepoSettingsStore) Get(ctx context.Context, repoFullName string) (sqlcgen.RepoSetting, error) {
	return s.q.GetRepoSettings(ctx, repoFullName)
}

// Upsert idempotently creates-or-updates repoFullName's settings row with
// blockOnHighRisk/sentinelAutofixEnabled as the new, full current values
// (never a delta/patch -- see UpsertRepoSettings' own generated doc
// comment). sentinelAutofixEnabled (Step 48, §17.1) is this same table's
// own further admin-only, per-repo boolean -- migrations/000048's own doc
// comment.
func (s *RepoSettingsStore) Upsert(ctx context.Context, repoFullName string, blockOnHighRisk, sentinelAutofixEnabled bool) (sqlcgen.RepoSetting, error) {
	return s.q.UpsertRepoSettings(ctx, sqlcgen.UpsertRepoSettingsParams{
		RepoFullName:           repoFullName,
		BlockOnHighRisk:        blockOnHighRisk,
		SentinelAutofixEnabled: sentinelAutofixEnabled,
	})
}

// UpsertAutoMergeToggle idempotently creates-or-updates repoFullName's
// §21.2 stage-2 auto-merge toggle -- §62 review finding C5 (MEDIUM but a
// privilege boundary, fixed): touches ONLY auto_merge_enabled, leaving
// max_auto_approve_files_changed/sensitive_blast_radius_tags completely
// untouched (see UpsertAutoMergeToggle's own generated doc comment for
// the full "why" this replaces the previous, combined
// UpsertAutoApprovalSettings). Column-scoped: a concurrent
// UpsertAutoApprovalEligibility call for the SAME repo can never be
// clobbered by this write, or vice versa.
func (s *RepoSettingsStore) UpsertAutoMergeToggle(ctx context.Context, repoFullName string, autoMergeEnabled bool) (sqlcgen.RepoSetting, error) {
	return s.q.UpsertAutoMergeToggle(ctx, sqlcgen.UpsertAutoMergeToggleParams{
		RepoFullName:     repoFullName,
		AutoMergeEnabled: autoMergeEnabled,
	})
}

// UpsertAutoApprovalEligibility idempotently creates-or-updates
// repoFullName's §21.2 stage-1 eligibility config -- §62 review finding
// C5's own column-scoped sibling: touches ONLY
// max_auto_approve_files_changed/sensitive_blast_radius_tags, leaving
// auto_merge_enabled completely untouched (see
// UpsertAutoApprovalEligibility's own generated doc comment). nil
// maxAutoApproveFilesChanged / nil sensitiveBlastRadiusTagsJSON both mean
// "use the engine's own built-in default"
// (internal/domain/autoapproval.DefaultEligibilityConfig).
// sensitiveBlastRadiusTagsJSON is pre-marshaled JSON bytes (a JSON array
// of review.Tag strings) -- this store does no JSON encoding of its own,
// mirroring this package's own "thin, pass-through, no business rules"
// discipline; the caller (internal/app/reviewverdict) owns the
// review.Tag <-> JSON conversion.
func (s *RepoSettingsStore) UpsertAutoApprovalEligibility(ctx context.Context, repoFullName string, maxAutoApproveFilesChanged *int32, sensitiveBlastRadiusTagsJSON []byte) (sqlcgen.RepoSetting, error) {
	return s.q.UpsertAutoApprovalEligibility(ctx, sqlcgen.UpsertAutoApprovalEligibilityParams{
		RepoFullName:               repoFullName,
		MaxAutoApproveFilesChanged: maxAutoApproveFilesChanged,
		SensitiveBlastRadiusTags:   sensitiveBlastRadiusTagsJSON,
	})
}

// UpsertAutoRetriggerReviewToggle idempotently creates-or-updates
// repoFullName's §24.5 per-repo opt-in -- COLUMN-SCOPED (mirrors
// UpsertAutoMergeToggle's own identical shape, §62 review finding C5's
// pattern generalized to this further, independently-gated toggle):
// touches ONLY auto_retrigger_review_enabled, leaving every other
// repo_settings column completely untouched.
func (s *RepoSettingsStore) UpsertAutoRetriggerReviewToggle(ctx context.Context, repoFullName string, enabled bool) (sqlcgen.RepoSetting, error) {
	return s.q.UpsertAutoRetriggerReviewToggle(ctx, sqlcgen.UpsertAutoRetriggerReviewToggleParams{
		RepoFullName:               repoFullName,
		AutoRetriggerReviewEnabled: enabled,
	})
}

// UpsertDescriptionAutofixToggle idempotently creates-or-updates
// repoFullName's §26.2 per-repo opt-in -- COLUMN-SCOPED (mirrors
// UpsertAutoMergeToggle/UpsertAutoRetriggerReviewToggle's own identical
// shape, §62 review finding C5's pattern generalized to this further,
// independently-gated toggle): touches ONLY description_autofix_enabled,
// leaving every other repo_settings column completely untouched.
func (s *RepoSettingsStore) UpsertDescriptionAutofixToggle(ctx context.Context, repoFullName string, enabled bool) (sqlcgen.RepoSetting, error) {
	return s.q.UpsertDescriptionAutofixToggle(ctx, sqlcgen.UpsertDescriptionAutofixToggleParams{
		RepoFullName:              repoFullName,
		DescriptionAutofixEnabled: enabled,
	})
}

// UpsertReviewDepthConfig idempotently creates-or-updates repoFullName's
// §26.3 reviewDepth config -- COLUMN-SCOPED (mirrors
// UpsertAutoMergeToggle/UpsertAutoRetriggerReviewToggle/
// UpsertDescriptionAutofixToggle's own identical shape, §62 review
// finding C5's pattern generalized to this further, independently-gated
// config): touches ONLY review_depth_mode/review_depth_deep_paths,
// leaving every other repo_settings column completely untouched.
// deepPathsJSON is pre-marshaled JSON bytes (a JSON array of glob-pattern
// strings) -- this store does no JSON encoding of its own, mirroring
// UpsertAutoApprovalEligibility's own identical "caller owns the
// encoding" convention.
func (s *RepoSettingsStore) UpsertReviewDepthConfig(ctx context.Context, repoFullName string, mode *string, deepPathsJSON []byte) (sqlcgen.RepoSetting, error) {
	return s.q.UpsertReviewDepthConfig(ctx, sqlcgen.UpsertReviewDepthConfigParams{
		RepoFullName:         repoFullName,
		ReviewDepthMode:      mode,
		ReviewDepthDeepPaths: deepPathsJSON,
	})
}

// ListAutoMergeEnabled returns every repo_settings row with
// auto_merge_enabled = true -- internal/app/automerge's own per-tick
// repo enumeration (see ListAutoMergeEnabledRepos' own generated doc
// comment for why this is the one enumeration source available).
func (s *RepoSettingsStore) ListAutoMergeEnabled(ctx context.Context) ([]sqlcgen.RepoSetting, error) {
	return s.q.ListAutoMergeEnabledRepos(ctx)
}

// UpsertPreviewSettings idempotently creates-or-updates repoFullName's RWX
// preview configuration (Step 57, §4.1.2 point 1) -- dispatchKey/
// endpointTemplate/orgSlug as the new, full current values for those THREE
// columns only, leaving block_on_high_risk/sentinel_autofix_enabled
// completely untouched (UpsertRWXPreviewSettings' own generated doc
// comment). No admin-facing REST route calls this yet (Step 57's own
// scope is the dispatch/notifier mechanism, not a settings UI) -- today's
// one real caller is this package's own integration tests, exercising the
// exact write path a future settings endpoint would use.
func (s *RepoSettingsStore) UpsertPreviewSettings(ctx context.Context, repoFullName, dispatchKey, endpointTemplate, orgSlug string) (sqlcgen.RepoSetting, error) {
	return s.q.UpsertRWXPreviewSettings(ctx, sqlcgen.UpsertRWXPreviewSettingsParams{
		RepoFullName:               repoFullName,
		RwxPreviewDispatchKey:      &dispatchKey,
		RwxPreviewEndpointTemplate: &endpointTemplate,
		RwxPreviewOrgSlug:          &orgSlug,
	})
}
