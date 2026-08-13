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

// ComputeDecision is Step 68's own "never-throw" entry point (§26.3: "ANY
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
// Returns the FRESH decision alone -- §24's re-review floor (Floor,
// internal/domain/reviewtriage/depth.go) is a SEPARATE, caller-applied
// step (see internal/app/sessionactor/reviewretrigger.go's own call
// site): a caller with no prior depth to floor against (a brand-new
// review session) simply uses decision.Depth as-is; a caller re-
// reviewing an existing PR calls reviewtriage.Floor(decision.Depth,
// priorDepth) and rebuilds its own DecisionRecord via reviewtriage.
// NewDecisionRecord(decision, cfg, flooredDepth) to capture that the
// floor is what actually decided.
func ComputeDecision(ctx context.Context, deps Deps, repoFullName string, prNumber int32, prCtx review.PreFetchedContext) (reviewtriage.Decision, reviewtriage.Config) {
	logger := platform.Logger(ctx)

	cfg, err := LoadConfig(ctx, deps, repoFullName)
	if err != nil {
		// LoadConfig's own doc comment already logs the read failure --
		// no second log line here, just the safe fallback.
		cfg = reviewtriage.DefaultConfig()
	}

	priorVerdictRiskHigh := false
	if latest, verdictErr := deps.ReviewVerdicts.GetLatest(ctx, repoFullName, prNumber); verdictErr != nil {
		if !errors.Is(verdictErr, pgx.ErrNoRows) {
			logger.Warn("reviewtriage: compute decision: read latest review verdict failed, treating prior-high-verdict signal as absent", "error", verdictErr, "repo_full_name", repoFullName, "pr_number", prNumber)
		}
		// pgx.ErrNoRows: no verdict has ever been posted for this PR --
		// priorVerdictRiskHigh correctly stays false, not an error at all.
	} else {
		priorVerdictRiskHigh = latest.RiskLevel == string(review.RiskLevelHigh)
	}

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
// LabelNeedsHuman (Step 47's own maintainer escape hatch) -- see
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
