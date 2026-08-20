package reviewtriage

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/domain/reviewpost"
	"github.com/khazaddev/narvi/internal/domain/reviewtriage"
	"github.com/khazaddev/narvi/internal/platform"
)

// ComputeDecision is §26.3's own "never-throw" entry point (§26.3: "ANY
// triage error fails open to light -- a review must never be blocked by
// its own router"), mirroring internal/app/intentclassifier.Service.
// ClassifyAndRecord's own identical "the caller never sees an error, only
// a safe, always-valid result" contract (§18.1). NO error return at all,
// by construction: every internal failure this function's own body can
// hit (a repo_settings read failure in LoadConfig, a review_verdicts read
// failure below) is caught, logged, and neutralized to the value that
// makes reviewtriage.Decide route light on its own -- there is no path
// through this function's own body that can reach its return statement
// with anything OTHER than a valid Decision/Config pair.
//
// prCtx is the SAME review.PreFetchedContext every review-trigger path
// already builds via internal/app/reviewcontext.Fetch (§26.3: "already
// fetched with the inline diff at review-session creation") -- this
// function performs exactly ONE further read of its own (the latest
// posted verdict for repoFullName/prNumber, for the "prior high verdict"
// signal) plus LoadConfig's own repo_settings read; no new network call,
// no diff re-fetch.
//
// Returns the FRESH decision, cfg, and priorReviewDepth -- priorReviewDepth
// (adversarial-review fix, D1: "re-review depth floor applied at only 1 of
// 3 lanes") is the SAME latest-review_verdicts row's own review_path this
// function already reads (below) for the "prior high verdict" signal
// (rule 4), now ALSO surfaced to the caller instead of discarded, so
// every caller -- not just internal/app/sessionactor/reviewretrigger.go's
// own auto-retrigger lane, which already performed a SEPARATE GetLatest
// read of its own for exactly this purpose -- can apply §24's re-review
// floor (reviewtriage.Floor(decision.Depth, priorReviewDepth)) without a
// second, redundant Postgres query. Empty ("") when no verdict has ever
// been posted for this PR, or when the latest one predates Step 68 (its
// own turn never resolved a depth) -- both degrade identically to
// "nothing to floor against" (reviewtriage.Floor's own doc comment: an
// empty/unrecognized prior ranks with DepthLight, the least conservative
// reading), exactly like a brand-new review session with no prior turn at
// all.
//
// A caller with no prior depth to floor against (a brand-new review
// session, priorReviewDepth == "") simply uses decision.Depth as-is --
// reviewtriage.Floor(fresh, "") is a no-op by construction (Floor's own
// rank table), so a caller MAY also apply Floor unconditionally without
// special-casing this case itself. A caller re-reviewing an existing PR
// calls reviewtriage.Floor(decision.Depth, priorReviewDepth) and rebuilds
// its own DecisionRecord via reviewtriage.NewDecisionRecord(decision, cfg,
// flooredDepth) to capture that the floor is what actually decided.
func ComputeDecision(ctx context.Context, deps Deps, repoFullName string, prNumber int32, prCtx review.PreFetchedContext) (decision reviewtriage.Decision, cfg reviewtriage.Config, priorReviewDepth reviewtriage.ReviewDepth) {
	logger := platform.Logger(ctx)

	cfg, err := LoadConfig(ctx, deps, repoFullName)
	if err != nil {
		// LoadConfig's own doc comment already logs the read failure --
		// no second log line here, just the safe fallback.
		cfg = reviewtriage.DefaultConfig()
	}

	// A nil deps.ReviewVerdicts (this package's own tests, or any other
	// minimal wiring that doesn't care about this Step) degrades
	// identically to "no verdict on record" -- never a panic, mirroring
	// LoadConfig's own identical nil-store convention (config.go).
	priorVerdictRiskHigh := false
	if deps.ReviewVerdicts == nil {
		decision, cfg = decideWithSignals(prCtx, cfg, priorVerdictRiskHigh)
		return decision, cfg, ""
	}
	if latest, verdictErr := deps.ReviewVerdicts.GetLatest(ctx, repoFullName, prNumber); verdictErr != nil {
		if !errors.Is(verdictErr, pgx.ErrNoRows) {
			logger.Warn("reviewtriage: compute decision: read latest review verdict failed, treating prior-high-verdict signal as absent", "error", verdictErr, "repo_full_name", repoFullName, "pr_number", prNumber)
		}
		// pgx.ErrNoRows: no verdict has ever been posted for this PR --
		// priorVerdictRiskHigh correctly stays false, priorReviewDepth
		// correctly stays "", neither is an error at all.
	} else {
		priorVerdictRiskHigh = latest.RiskLevel == string(review.RiskLevelHigh)
		if latest.ReviewPath != nil {
			priorReviewDepth = reviewtriage.ReviewDepth(*latest.ReviewPath)
		}
	}

	decision, cfg = decideWithSignals(prCtx, cfg, priorVerdictRiskHigh)
	return decision, cfg, priorReviewDepth
}

// decideWithSignals assembles the final reviewtriage.Signals from prCtx
// and the already-resolved priorVerdictRiskHigh, and calls the pure
// domain Decide -- the one tail both of ComputeDecision's own paths
// (real deps.ReviewVerdicts read, or a nil-store short-circuit) share.
func decideWithSignals(prCtx review.PreFetchedContext, cfg reviewtriage.Config, priorVerdictRiskHigh bool) (reviewtriage.Decision, reviewtriage.Config) {
	sig := reviewtriage.Signals{
		Additions:              prCtx.Additions,
		Deletions:              prCtx.Deletions,
		ChangedPaths:           prCtx.ChangedPaths,
		NeedsHumanLabelPresent: hasNeedsHumanLabel(prCtx.Labels),
		PriorVerdictRiskHigh:   priorVerdictRiskHigh,
	}
	return reviewtriage.Decide(sig, cfg), cfg
}

// hasNeedsHumanLabel reports whether labels contains reviewpost.
// LabelNeedsHuman (§8.2's own maintainer escape hatch) -- see
// internal/domain/reviewtriage/doc.go's own "v1 rules -- five, not
// three" section for why this is one of Decide's triggers.
func hasNeedsHumanLabel(labels []string) bool {
	for _, l := range labels {
		if l == reviewpost.LabelNeedsHuman {
			return true
		}
	}
	return false
}
