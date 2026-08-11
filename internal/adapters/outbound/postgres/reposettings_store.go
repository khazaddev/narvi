package postgres

import (
	"context"

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

// UpsertAutoApprovalSettings idempotently creates-or-updates repoFullName's
// §21.2 auto-approval eligibility config + auto-merge toggle -- autoMergeEnabled
// as the new, full current value for that column; maxAutoApproveFilesChanged
// nil / sensitiveBlastRadiusTagsJSON nil both mean "use the engine's own
// built-in default" (internal/domain/autoapproval.DefaultEligibilityConfig)
// -- see UpsertAutoApprovalSettings' own generated doc comment.
// sensitiveBlastRadiusTagsJSON is pre-marshaled JSON bytes (a JSON array
// of review.Tag strings) -- this store does no JSON encoding of its own,
// mirroring this package's own "thin, pass-through, no business rules"
// discipline; the caller (internal/app/reviewverdict) owns the
// review.Tag <-> JSON conversion.
func (s *RepoSettingsStore) UpsertAutoApprovalSettings(ctx context.Context, repoFullName string, autoMergeEnabled bool, maxAutoApproveFilesChanged *int32, sensitiveBlastRadiusTagsJSON []byte) (sqlcgen.RepoSetting, error) {
	return s.q.UpsertAutoApprovalSettings(ctx, sqlcgen.UpsertAutoApprovalSettingsParams{
		RepoFullName:               repoFullName,
		AutoMergeEnabled:           autoMergeEnabled,
		MaxAutoApproveFilesChanged: maxAutoApproveFilesChanged,
		SensitiveBlastRadiusTags:   sensitiveBlastRadiusTagsJSON,
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
