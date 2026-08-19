package seed

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/khazaddev/narvi/internal/app/auditlog"
	"github.com/khazaddev/narvi/internal/domain/seedmanifest"
)

// seedRepoSetting reconciles ONE repoSettings manifest entry against
// repo_settings (reconcile-to-declared -- see doc.go). Every field on
// seedmanifest.RepoSetting is an independent *bool: a nil field is left
// completely untouched by this function (nothing is written for it at
// all), matching this table's own existing column-scoped Upsert*
// precedent (UpsertAutoMergeToggle et al. in internal/adapters/outbound/
// postgres/reposettings_store.go). block_on_high_risk/
// sentinel_autofix_enabled are the one EXCEPTION: RepoSettingsStore.
// Upsert writes both together (it predates the column-scoped
// convention, §62 review finding C5's own fix only landed for the LATER
// toggles) -- so when only one of the two is declared, this function
// first reads the current row to carry the undeclared one through
// unchanged, rather than resetting it to false.
func seedRepoSetting(ctx context.Context, deps Deps, s seedmanifest.RepoSetting, dryRun bool) Item {
	key := s.RepoFullName

	current, err := deps.RepoSettings.Get(ctx, s.RepoFullName)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Item{Kind: "repo_setting", Key: key, Outcome: OutcomeError, Detail: "read current settings: " + err.Error()}
	}
	// A missing row (pgx.ErrNoRows) means every column defaults to its
	// own safe zero value -- current is already the zero sqlcgen.
	// RepoSetting in that case, matching this table's own established
	// "absent row = every flag off" convention (migrations/
	// 000044_repo_settings.up.sql's own doc comment).

	var changed []string
	if s.BlockOnHighRisk != nil {
		changed = append(changed, fmt.Sprintf("blockOnHighRisk=%v", *s.BlockOnHighRisk))
	}
	if s.SentinelAutofixEnabled != nil {
		changed = append(changed, fmt.Sprintf("sentinelAutofixEnabled=%v", *s.SentinelAutofixEnabled))
	}
	if s.AutoMergeEnabled != nil {
		changed = append(changed, fmt.Sprintf("autoMergeEnabled=%v", *s.AutoMergeEnabled))
	}
	if s.AutoRetriggerReview != nil {
		changed = append(changed, fmt.Sprintf("autoRetriggerReviewEnabled=%v", *s.AutoRetriggerReview))
	}
	if s.DescriptionAutofix != nil {
		changed = append(changed, fmt.Sprintf("descriptionAutofixEnabled=%v", *s.DescriptionAutofix))
	}
	if len(changed) == 0 {
		return Item{Kind: "repo_setting", Key: key, Outcome: OutcomeSkipped, Detail: "no fields declared"}
	}
	detail := strings.Join(changed, ", ")

	if dryRun {
		return Item{Kind: "repo_setting", Key: key, Outcome: OutcomeWouldUpsert, Detail: detail}
	}

	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return Item{Kind: "repo_setting", Key: key, Outcome: OutcomeError, Detail: "begin tx: " + err.Error()}
	}
	defer func() { _ = tx.Rollback(ctx) }()
	store := deps.RepoSettings.WithTx(tx)

	if s.BlockOnHighRisk != nil || s.SentinelAutofixEnabled != nil {
		block := current.BlockOnHighRisk
		if s.BlockOnHighRisk != nil {
			block = *s.BlockOnHighRisk
		}
		sentinel := current.SentinelAutofixEnabled
		if s.SentinelAutofixEnabled != nil {
			sentinel = *s.SentinelAutofixEnabled
		}
		if _, err := store.Upsert(ctx, s.RepoFullName, block, sentinel); err != nil {
			return Item{Kind: "repo_setting", Key: key, Outcome: OutcomeError, Detail: "upsert block/sentinel: " + err.Error()}
		}
	}
	if s.AutoMergeEnabled != nil {
		if _, err := store.UpsertAutoMergeToggle(ctx, s.RepoFullName, *s.AutoMergeEnabled); err != nil {
			return Item{Kind: "repo_setting", Key: key, Outcome: OutcomeError, Detail: "upsert auto-merge toggle: " + err.Error()}
		}
	}
	if s.AutoRetriggerReview != nil {
		if _, err := store.UpsertAutoRetriggerReviewToggle(ctx, s.RepoFullName, *s.AutoRetriggerReview); err != nil {
			return Item{Kind: "repo_setting", Key: key, Outcome: OutcomeError, Detail: "upsert auto-retrigger-review toggle: " + err.Error()}
		}
	}
	if s.DescriptionAutofix != nil {
		if _, err := store.UpsertDescriptionAutofixToggle(ctx, s.RepoFullName, *s.DescriptionAutofix); err != nil {
			return Item{Kind: "repo_setting", Key: key, Outcome: OutcomeError, Detail: "upsert description-autofix toggle: " + err.Error()}
		}
	}

	if err := auditlog.Record(ctx, deps.AuditLog.WithTx(tx), systemActor(), "seed.repo_setting_upserted", "repo_settings", s.RepoFullName, map[string]any{
		"fields": changed,
	}); err != nil {
		return Item{Kind: "repo_setting", Key: key, Outcome: OutcomeError, Detail: "record audit log: " + err.Error()}
	}

	if err := tx.Commit(ctx); err != nil {
		return Item{Kind: "repo_setting", Key: key, Outcome: OutcomeError, Detail: "commit tx: " + err.Error()}
	}

	return Item{Kind: "repo_setting", Key: key, Outcome: OutcomeUpserted, Detail: detail}
}

// seedRWXPreview reconciles ONE rwxPreview manifest entry against
// repo_settings.rwx_preview_* (reconcile-to-declared, always all 3
// fields together -- see doc.go's own "integrations" scope writeup for
// why this table is this tool's answer to Step 75's "integrations"
// checklist item).
func seedRWXPreview(ctx context.Context, deps Deps, e seedmanifest.RWXPreview, dryRun bool) Item {
	key := e.RepoFullName
	detail := fmt.Sprintf("orgSlug=%s", e.OrgSlug)

	if dryRun {
		return Item{Kind: "rwx_preview", Key: key, Outcome: OutcomeWouldUpsert, Detail: detail}
	}

	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return Item{Kind: "rwx_preview", Key: key, Outcome: OutcomeError, Detail: "begin tx: " + err.Error()}
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := deps.RepoSettings.WithTx(tx).UpsertPreviewSettings(ctx, e.RepoFullName, e.DispatchKey, e.EndpointTemplate, e.OrgSlug); err != nil {
		return Item{Kind: "rwx_preview", Key: key, Outcome: OutcomeError, Detail: "upsert failed: " + err.Error()}
	}

	if err := auditlog.Record(ctx, deps.AuditLog.WithTx(tx), systemActor(), "seed.rwx_preview_upserted", "repo_settings", e.RepoFullName, map[string]any{
		"org_slug": e.OrgSlug,
	}); err != nil {
		return Item{Kind: "rwx_preview", Key: key, Outcome: OutcomeError, Detail: "record audit log: " + err.Error()}
	}

	if err := tx.Commit(ctx); err != nil {
		return Item{Kind: "rwx_preview", Key: key, Outcome: OutcomeError, Detail: "commit tx: " + err.Error()}
	}

	return Item{Kind: "rwx_preview", Key: key, Outcome: OutcomeUpserted, Detail: detail}
}
