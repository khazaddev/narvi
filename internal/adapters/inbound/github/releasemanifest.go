// This file (releasemanifest.go) wires Step 50's own ("release PR
// review", §15) release-PR detection (§15.1) and manifest check (§15.2)
// into this package's own webhook handler -- handler.go's own top-level
// call site, right after CreateOrJoin succeeds, mirrors this Step's own
// scoping decision: ONLY the WINNER (brand-new session) path ever
// triggers this, never the REUSE path -- see internal/app/releasereview's
// own top doc comment for the full "why".

package github

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/app/releasereview"
	intentdomain "github.com/khazaddev/narvi/internal/domain/intent"
	"github.com/khazaddev/narvi/internal/domain/reposource"
	"github.com/khazaddev/narvi/internal/platform"
)

// triggerReleaseManifestCheckBestEffort detects whether repoFullName#
// prNumber is a release PR (§15.1) and, if so, runs the manifest check
// (§15.2) -- entirely best-effort: every failure here is logged and this
// function simply returns, exactly like this file's own sibling
// handoff-readiness/contract-drift best-effort helpers elsewhere in this
// codebase (internal/app/sessionactor/handoffsentinel.go's own identical
// "never block a spawn/push" discipline, applied here to "never block a
// webhook ack").
//
// cfg.SourceControl/cfg.Outbox being nil (this package's own
// handler_test.go, or any other minimal wiring that doesn't care about
// this Step) simply skips this entirely -- mirrors cfg.DiffFetcher's own
// identical nil-safe precedent.
//
// Detection re-fetches the PR's own CURRENT head/base branch and labels
// via ONE GetPullRequest call, rather than reusing whatever headBranch
// this mention's own payload happened to carry: labels are never present
// on any of this package's own parsed mention payloads today (payload.go
// parses only what §46/§17.6's own already-shipped concerns needed), and
// a fresh call here -- paid only on the WINNER path, i.e. once per
// brand-new review session, never once per ordinary mention -- is a
// small, bounded cost for a session-creation-time-only signal.
func triggerReleaseManifestCheckBestEffort(
	ctx context.Context,
	logger *slog.Logger,
	cfg Config,
	repoFullName string,
	prNumber int32,
	sessionID pgtype.UUID,
) {
	if cfg.SourceControl == nil || cfg.Outbox == nil {
		return
	}

	owner, repo, ok := reposource.SplitFullName(repoFullName)
	if !ok {
		logger.Warn("github: release manifest: could not split repo_full_name into owner/repo, skipping",
			"repo_full_name", repoFullName, "pr_number", prNumber)
		return
	}

	prCtx, cancel := context.WithTimeout(ctx, cfg.Timeouts.GitHubGetPRTimeout)
	pr, err := cfg.PullRequests.GetPullRequest(prCtx, owner, repo, prNumber, cfg.BotToken)
	cancel()
	if err != nil {
		logger.Warn("github: release manifest: fetch pull request (for release detection) failed, skipping",
			"error", err, "repo", repoFullName, "pr_number", prNumber)
		return
	}

	if !intentdomain.DetectRelease(pr.HeadRef, pr.BaseRef, pr.Labels, cfg.ReleaseBranchPattern, cfg.ReleaseLabel) {
		return
	}

	logger.Info("github: release PR detected, running manifest check",
		"repo", repoFullName, "pr_number", prNumber, "head", pr.HeadRef, "base", pr.BaseRef)

	var correlationID *string
	if id, ok := platform.CorrelationIDFromContext(ctx); ok && id != "" {
		correlationID = &id
	}

	releasereview.Run(ctx, logger, releasereview.Deps{
		SourceControl: cfg.SourceControl,
		Outbox:        cfg.Outbox,
		Timeouts:      cfg.Timeouts,
	}, releasereview.Input{
		SessionID:     sessionID,
		Owner:         owner,
		Repo:          repo,
		PRNumber:      prNumber,
		BaseRef:       pr.BaseRef,
		HeadRef:       pr.HeadRef,
		Token:         cfg.BotToken,
		CorrelationID: correlationID,
	})
}
