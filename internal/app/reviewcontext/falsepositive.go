package reviewcontext

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/domain/falsepositive"
)

// This file implements §22's own §22.3 advisory-injection half:
// "injected into every review pass (first pass and re-review alike) as an
// explicitly-untrusted, advisory content block." FetchFalsePositivePatterns
// is this package's OWN impure fetch, mirroring FetchAlreadyAnswered's own
// established "impure fetch here, pure render in a sibling domain package"
// split exactly (falsepositive.RenderAdvisoryBlock is the pure render
// half this function calls).

// FalsePositivePatternsFetcher is the narrow slice of
// *postgres.FalsePositivePatternStore this function needs -- a small,
// locally-defined interface (mirroring this package's own FindingsFetcher
// precedent, alreadyanswered.go) so a unit test can inject a fake with no
// real Postgres connection.
type FalsePositivePatternsFetcher interface {
	ListActive(ctx context.Context, repoFullName string) ([]sqlcgen.ReviewFalsePositivePattern, error)
	IncrementHitCount(ctx context.Context, ids []pgtype.UUID) error
}

// FetchFalsePositivePatterns fetches repoFullName's own currently-active
// (not retired) review_false_positive_patterns rows and renders them via
// falsepositive.RenderAdvisoryBlock -- best-effort, exactly like Fetch/
// FetchAlreadyAnswered: a fetch failure degrades to an empty string
// (logged, never propagated as an error, never a reason to fail the
// review turn's own creation), so this function never returns an error of
// its own, mirroring both siblings' own identical "never returns an error
// of its own" doc comment precedent -- §22.4's own "a failed pattern read
// yields no injected block" fail-safe requirement, satisfied by
// construction (an empty return is indistinguishable from "this repo has
// no active patterns", which is already the correct, safe rendering for
// EITHER case).
//
// Every pattern actually returned has its own hit_count/last_hit_at
// bumped, best-effort, immediately after a successful fetch (§22.4's own
// usage-signal bookkeeping) -- a failure on THIS secondary write is
// logged and otherwise ignored, never turning an already-successful fetch
// into a degraded one: the advisory block this function returns was
// already fully and correctly rendered from the READ, which succeeded.
//
// The caller is responsible for PREPENDING this function's own return
// value to basePrompt BEFORE calling review.RenderTurnPrompt, exactly
// like FetchAlreadyAnswered's own identical division of responsibility --
// this function itself never touches basePrompt or calls RenderTurnPrompt.
func FetchFalsePositivePatterns(ctx context.Context, logger *slog.Logger, fetcher FalsePositivePatternsFetcher, repoFullName string) string {
	if fetcher == nil {
		return ""
	}

	rows, err := fetcher.ListActive(ctx, repoFullName)
	if err != nil {
		logger.Warn("reviewcontext: fetch active false-positive patterns failed, review turn will carry no advisory block",
			"error", err, "repo_full_name", repoFullName)
		return ""
	}
	if len(rows) == 0 {
		return ""
	}

	patterns := make([]falsepositive.Pattern, len(rows))
	ids := make([]pgtype.UUID, len(rows))
	for i, row := range rows {
		patterns[i] = falsepositive.Pattern{Reason: row.Reason}
		ids[i] = row.ID
	}

	if err := fetcher.IncrementHitCount(ctx, ids); err != nil {
		logger.Warn("reviewcontext: increment false-positive pattern hit count failed, advisory block still rendered",
			"error", err, "repo_full_name", repoFullName)
	}

	return falsepositive.RenderAdvisoryBlock(patterns)
}
