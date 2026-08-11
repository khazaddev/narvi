// Package automerge implements §21.2 stage 2's own auto-merge worker --
// the ONLY thing an armed repo_settings.auto_merge_enabled toggle
// actually changes (§21.2: "Auto-approval alone does not merge
// anything"). Runs as a background pump, mirroring internal/app/
// automation.Engine's own identical periodic-tick shape, discovering
// candidate PRs from review_verdicts' own bounded history (a cheap,
// local Postgres read -- never a GitHub call for DISCOVERY) and then
// re-confirming each one LIVE via internal/app/decisioninbox.
// RevalidateForAutoMerge -- the SAME server-side re-validation-at-click
// contract the human-clicked Merge endpoint already uses (§21.2: "a
// deliberate reuse, not a parallel merge path"), just machine-initiated
// with the deployment's own bot token instead of any human's.
package automerge

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sync/errgroup"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/app/auditlog"
	"github.com/khazaddev/narvi/internal/app/decisioninbox"
	"github.com/khazaddev/narvi/internal/app/ports"
	appreviewverdict "github.com/khazaddev/narvi/internal/app/reviewverdict"
	"github.com/khazaddev/narvi/internal/domain/reposource"
	"github.com/khazaddev/narvi/internal/platform"
)

// maxCandidatesPerRepoPerTick bounds how many auto-approved candidate PRs
// ONE repo contributes to ONE tick -- §21.1's own "bounded from day one"
// discipline, applied here to a per-repo, per-tick cap so one repo with
// an unusually large recent-verdict history can never starve every other
// armed repo's own tick out of the SAME pass.
const maxCandidatesPerRepoPerTick = 20

// Deps bundles every dependency Worker needs -- constructed once at
// process wiring time (cmd/control-plane/main.go), mirroring every other
// app-layer Deps struct in this codebase.
type Deps struct {
	DecisionInbox decisioninbox.Deps
	SourceControl ports.SourceControl
	AuditLog      *postgres.AuditLogStore

	BotToken string
	Timeouts platform.Timeouts
}

// Worker runs the auto-merge pump.
type Worker struct {
	deps Deps
}

// New builds a Worker.
func New(deps Deps) *Worker {
	return &Worker{deps: deps}
}

// Run ticks every deps.Timeouts.AutoMergePumpInterval until ctx is
// cancelled -- mirrors internal/app/automation.Engine.Run's own identical
// ticker-loop shape (errgroup + context, no naked goroutine, CLAUDE.md/
// §11).
func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.deps.Timeouts.AutoMergePumpInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := w.PumpOnce(ctx, time.Now()); err != nil {
				platform.Logger(ctx).Error("automerge: pump tick failed", "error", err)
			}
		}
	}
}

// PumpOnce runs one full tick: every repo with auto_merge_enabled=true,
// every one of that repo's own bounded-recent auto-approved candidates,
// each re-confirmed live and merged if still eligible. Every repo/
// candidate is independently best-effort (errgroup fans them out, but a
// single candidate's own failure never aborts the others) -- mirrors
// buildAttentionItems' own "each sub-scan independently best-effort"
// precedent (internal/app/decisioninbox/aggregate.go).
func (w *Worker) PumpOnce(ctx context.Context, now time.Time) error {
	repos, err := w.deps.DecisionInbox.ReviewVerdict.RepoSettings.ListAutoMergeEnabled(ctx)
	if err != nil {
		return fmt.Errorf("automerge: list auto-merge-enabled repos: %w", err)
	}

	g, gctx := errgroup.WithContext(ctx)
	for _, repo := range repos {
		repo := repo
		g.Go(func() error {
			w.pumpRepo(gctx, repo.RepoFullName, now)
			return nil
		})
	}
	return g.Wait()
}

// pumpRepo handles one repo's own candidates -- logged, never propagated,
// since PumpOnce's own errgroup fans repos out independently and a
// single repo's own failure must never abort every other armed repo's
// tick.
func (w *Worker) pumpRepo(ctx context.Context, repoFullName string, now time.Time) {
	since := now.Add(-w.deps.Timeouts.AutoMergeCandidateLookback)
	candidates, err := w.deps.DecisionInbox.ReviewVerdict.ReviewVerdicts.ListLatestAutoApproved(ctx, repoFullName, pgtype.Timestamptz{Time: since, Valid: true}, maxCandidatesPerRepoPerTick)
	if err != nil {
		platform.Logger(ctx).Error("automerge: list latest auto-approved verdicts failed", "error", err, "repo_full_name", repoFullName)
		return
	}

	for _, candidate := range candidates {
		w.mergeCandidate(ctx, repoFullName, int(candidate.PrNumber))
	}
}

// mergeCandidate re-confirms one candidate live and merges it if still
// eligible -- every outcome (ineligible, revalidation error, merge
// error, success) is logged; a merge audit-log row is written with NO
// human actor (pgtype.UUID{}, the zero value), mirroring §17.5's own
// "the same allowance already made in the audit_log schema for actions
// with no human actor" precedent (internal/adapters/inbound/github/
// pullrequestevent.go's own sentinel-fix merge-gate-evaluated row).
func (w *Worker) mergeCandidate(ctx context.Context, repoFullName string, prNumber int) {
	logger := platform.Logger(ctx)

	ok, headSHA, reason, err := decisioninbox.RevalidateForAutoMerge(ctx, w.deps.DecisionInbox, w.deps.SourceControl, repoFullName, prNumber, w.deps.BotToken)
	if err != nil {
		logger.Error("automerge: revalidate for auto-merge failed", "error", err, "repo_full_name", repoFullName, "pr_number", prNumber)
		return
	}
	if !ok {
		logger.Info("automerge: candidate no longer eligible", "repo_full_name", repoFullName, "pr_number", prNumber, "reason", reason)
		return
	}

	owner, repo, splitOK := reposource.SplitFullName(repoFullName)
	if !splitOK {
		logger.Error("automerge: repoFullName not shaped owner/repo", "repo_full_name", repoFullName)
		return
	}

	mergeCtx, cancel := context.WithTimeout(ctx, w.deps.Timeouts.GitHubMergePRTimeout)
	mergeSHA, err := w.deps.SourceControl.MergePR(mergeCtx, ports.MergePRSpec{
		Owner: owner, Repo: repo, Number: prNumber, HeadSHA: headSHA, Token: w.deps.BotToken,
	})
	cancel()
	if err != nil {
		logger.Error("automerge: merge pr failed", "error", err, "repo_full_name", repoFullName, "pr_number", prNumber)
		return
	}

	logger.Info("automerge: merged", "repo_full_name", repoFullName, "pr_number", prNumber, "merge_commit_sha", mergeSHA)

	appreviewverdict.RecordConfirmed(ctx, w.deps.DecisionInbox.ReviewVerdict, repoFullName, int32(prNumber), headSHA)

	if err := auditlog.Record(ctx, w.deps.AuditLog, pgtype.UUID{}, "auto_merge.merged", "pull_request", fmt.Sprintf("%s#%d", repoFullName, prNumber), map[string]any{
		"repo_full_name":   repoFullName,
		"pr_number":        prNumber,
		"merge_commit_sha": mergeSHA,
	}); err != nil {
		// The merge already succeeded on GitHub -- a logging failure here
		// must never claim otherwise, mirroring httpapi.MergePullRequest's
		// own identical posture for the human-clicked path.
		logger.Error("automerge: record audit log for merge failed", "error", err, "repo_full_name", repoFullName, "pr_number", prNumber)
	}
}
