package reviewcontext

import (
	"context"
	"log/slog"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/domain/reviewpost"
)

// This file implements Step 48's own ("sentinels + suggestions", §22.1)
// re-review reconciliation half: "feeding re-review reconciliation as
// deterministic 'already answered' facts prepended to (not replacing) the
// prose fallback." FetchAlreadyAnswered is this package's OWN impure
// fetch (a real Postgres read), mirroring Fetch's own established "impure
// fetch here, pure render in internal/domain/review/reviewpost" split
// exactly -- reviewpost.RenderAlreadyAnsweredFacts (reconcile.go) is the
// pure render half this function calls.

// FindingsFetcher is the narrow slice of *postgres.ReviewFindingStore this
// function needs -- a small, locally-defined interface (mirroring this
// package's own Fetcher precedent, fetch.go) so a unit test can inject a
// fake with no real Postgres connection.
type FindingsFetcher interface {
	ListOpenAndRebutted(ctx context.Context, repoFullName string, prNumber int32) ([]sqlcgen.ReviewFinding, error)
}

// FetchAlreadyAnswered fetches owner/repo#number's own currently open+
// rebutted review_findings rows and renders them via reviewpost.
// RenderAlreadyAnsweredFacts -- best-effort, exactly like Fetch above: a
// fetch failure degrades to an empty string (logged, never propagated as
// an error, never a reason to fail the review turn's own creation), so
// this function never returns an error of its own, mirroring Fetch's own
// "never returns an error of its own" doc comment precedent to the
// letter.
//
// The caller is responsible for PREPENDING this function's own return
// value to basePrompt BEFORE calling review.RenderTurnPrompt (§22.1's own
// "prepended to, never replacing, the prose fallback" requirement) --
// this function itself never touches basePrompt or calls RenderTurnPrompt,
// matching Fetch's own identical "assembles context, the CALLER decides
// how to fold it into the turn's own prompt" division of responsibility.
func FetchAlreadyAnswered(ctx context.Context, logger *slog.Logger, fetcher FindingsFetcher, repoFullName string, prNumber int32) string {
	if fetcher == nil {
		return ""
	}

	rows, err := fetcher.ListOpenAndRebutted(ctx, repoFullName, prNumber)
	if err != nil {
		logger.Warn("reviewcontext: fetch already-answered findings failed, review turn will carry no reconciliation facts",
			"error", err, "repo_full_name", repoFullName, "pr_number", prNumber)
		return ""
	}

	findings := make([]reviewpost.ReconciledFinding, len(rows))
	for i, row := range rows {
		var sentinelKind *reviewpost.SentinelKind
		if row.SentinelKind != nil {
			k := reviewpost.SentinelKind(*row.SentinelKind)
			sentinelKind = &k
		}
		findings[i] = reviewpost.ReconciledFinding{
			IdentityHash: row.IdentityHash,
			SentinelKind: sentinelKind,
			FilePath:     row.FilePath,
			Description:  row.Description,
			Status:       reviewpost.FindingStatus(row.Status),
			RebuttalText: row.RebuttalText,
		}
	}

	return reviewpost.RenderAlreadyAnsweredFacts(findings)
}
