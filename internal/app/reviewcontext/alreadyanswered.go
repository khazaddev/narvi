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
//
// changedPaths (§22.1.2's own "determinable fact" refinement) is the
// CURRENT diff's own changed-path list -- review.PreFetchedContext.
// ChangedPaths, Step 68's reviewtriage.ExtractChangedPaths -- forwarded
// verbatim to reviewpost.RenderAlreadyAnsweredFacts, this function's own
// pure render half, which uses it to mark (never drop, see that
// function's own doc comment for the full "why") a finding whose
// anchoring file has left the diff entirely. Every real caller of THIS
// function already has that value in hand from its own review.
// PreFetchedContext by the point it calls this function -- see each
// caller's own comment for why (internal/app/sessionactor/
// reviewretrigger.go's reviewCtx is fetched before composeAutoRetriggerPrompt
// calls this function; internal/adapters/inbound/httpapi/reviewretrigger.go
// and internal/adapters/inbound/github/handler.go both now call this
// function AFTER their own prCtx fetch, not before, exactly so
// prCtx.ChangedPaths is already resolved here -- a deliberate reordering
// of this Step, since neither the Postgres read this function performs
// nor the GitHub fetch that produces prCtx depends on the other, so
// reordering them changes nothing about correctness, only about when the
// retirement fact becomes available). A caller with no pre-fetched
// context at all (a nil diffFetcher, or a repo_full_name that fails to
// split) simply passes nil -- this function's own zero value -- and
// retirement is skipped exactly like a failed diff fetch would skip it
// (RenderAlreadyAnsweredFacts' own "nil means no reliable data" contract).
//
// diffTruncated (D1, adversarial review of PR #182, BLOCKING) is that
// SAME review.PreFetchedContext's own DiffTruncated -- forwarded verbatim
// alongside changedPaths, for the identical reason: a truncated diff's
// own changedPaths is a genuine but PARTIAL prefix of the real diff (git
// emits file sections in path-sorted order, so everything past the
// fetch's own byte-size cut is simply missing), and treating a partial
// list as if it were complete is exactly the "confidently gone from the
// diff" misreading RenderAlreadyAnsweredFacts' own doc comment now
// forbids (D1's own root cause: the changed-path list must be treated as
// authoritative-or-absent, never partial). Every real caller already has
// this value in hand at the SAME point it has changedPaths -- both come
// from the SAME review.PreFetchedContext.
func FetchAlreadyAnswered(ctx context.Context, logger *slog.Logger, fetcher FindingsFetcher, repoFullName string, prNumber int32, changedPaths []string, diffTruncated bool) string {
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

	return reviewpost.RenderAlreadyAnsweredFacts(findings, changedPaths, diffTruncated)
}
