package reviewtriage

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/domain/reviewtriage"
	"github.com/khazaddev/narvi/internal/platform"
)

// LoadConfig resolves repoFullName's own §26.3 reviewDepth config.
// Mirrors internal/app/reviewverdict.LoadEligibilityConfig's own three-
// outcome shape exactly:
//
//   - A MISSING repo_settings row (pgx.ErrNoRows) resolves to
//     reviewtriage.DefaultConfig(), err=nil -- a legitimate, common
//     "never configured yet" state.
//   - A NULL column on an EXISTING row resolves to that ONE field's own
//     default, err=nil.
//   - A GENUINE read error returns reviewtriage.Config{}, a non-nil
//     error -- UNLIKE LoadEligibilityConfig (an auto-merge gate, which
//     must fail CLOSED), ComputeDepth (compute.go) is this function's
//     ONE caller and treats ANY error here as "use DefaultConfig()" --
//     §26.3's own "any triage error fails open to light" rule means a
//     degraded config read must never block a review, so failing open
//     to the built-in default is the correct direction here, the
//     opposite of LoadEligibilityConfig's own unattended-merge context.
//
// A nil deps.RepoSettings (this package's own tests, or any other
// minimal wiring that doesn't care about this Step) degrades identically
// to a missing row -- DefaultConfig(), nil -- never a panic, mirroring
// ResolveProvenance's own identical nil-store convention (provenance.go).
func LoadConfig(ctx context.Context, deps Deps, repoFullName string) (reviewtriage.Config, error) {
	if deps.RepoSettings == nil {
		return reviewtriage.DefaultConfig(), nil
	}
	settings, err := deps.RepoSettings.Get(ctx, repoFullName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return reviewtriage.DefaultConfig(), nil
		}
		platform.Logger(ctx).Warn("reviewtriage: load config: read repo settings failed, falling open to the built-in default", "error", err, "repo_full_name", repoFullName)
		return reviewtriage.Config{}, err
	}

	cfg := reviewtriage.DefaultConfig()
	if settings.ReviewDepthMode != nil {
		cfg.Mode = reviewtriage.Mode(*settings.ReviewDepthMode)
	}
	if len(settings.ReviewDepthDeepPaths) > 0 {
		var paths []string
		if err := json.Unmarshal(settings.ReviewDepthDeepPaths, &paths); err == nil {
			cfg.DeepPaths = paths
		}
		// A malformed column (should never happen for a column only ever
		// written by UpsertReviewDepthConfig, defended against anyway)
		// degrades to no deepPaths at all -- never a decode error
		// propagated, mirroring internal/app/reviewverdict.unmarshalTags'
		// own identical "malformed/absent degrades to an empty, safe
		// value" precedent. Losing a repo's own EXTRA deep-routing paths
		// is the safe direction here too: the fixed sensitive-glob set
		// and the line/root thresholds still apply regardless.
	}

	// CostBudget (§26.7, Step 69): each of the two columns independently
	// overrides ONLY its own DefaultCostBudget field when Valid -- an
	// admin who configured only review_cost_budget_deep_usd (leaving
	// review_cost_budget_light_usd NULL) still gets the built-in $0.50
	// light default, never a zeroed-out light ceiling as a side effect of
	// configuring the other one, mirroring Mode/DeepPaths' own identical
	// "each field its own independent override" treatment immediately
	// above.
	if v, ok := NumericToFloat64(settings.ReviewCostBudgetLightUsd); ok {
		cfg.CostBudget.Light = v
	}
	if v, ok := NumericToFloat64(settings.ReviewCostBudgetDeepUsd); ok {
		cfg.CostBudget.Deep = v
	}
	return cfg, nil
}

// NumericToFloat64 converts one pgtype.Numeric column into a plain
// float64 -- ok=false for a SQL NULL (n.Valid == false, "use the built-in
// default", LoadConfig's own caller) or a value pgtype itself cannot
// represent as a float64 (should never happen for a column only ever
// written by UpsertReviewCostBudget's own bounded NUMERIC(10,2) writes,
// defended against anyway) -- both degrade identically to "no override",
// mirroring this file's own established "malformed/absent degrades to a
// safe default, never a propagated error" convention for
// ReviewDepthDeepPaths immediately above.
func NumericToFloat64(n pgtype.Numeric) (float64, bool) {
	if !n.Valid {
		return 0, false
	}
	f, err := n.Float64Value()
	if err != nil || !f.Valid {
		return 0, false
	}
	return f.Float64, true
}
