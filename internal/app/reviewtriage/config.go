package reviewtriage

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

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
	return cfg, nil
}
