// Package reviewtriage is the app-layer half of Step 68's own light/deep
// review-depth routing decision (§26.3) -- it aggregates Postgres (never
// itself I/O-free, unlike its sibling pure package internal/domain/
// reviewtriage, which this package calls but never duplicates the
// decision logic of) and NEVER lets a failure in that aggregation reach
// its one caller as an error: ComputeDepth (compute.go) has no error
// return at all, by construction, mirroring internal/app/intentclassifier's
// own "never-throw contract" (§18.1) -- see that function's own doc
// comment for the full "why" this satisfies §26.3's "ANY triage error
// fails open to light" requirement structurally, not by convention.
package reviewtriage

import (
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
)

// Deps bundles every dependency ComputeDepth needs -- constructed once at
// process wiring time (cmd/control-plane/main.go), mirroring every other
// app-layer Deps struct in this codebase (e.g. internal/app/reviewverdict.
// Deps).
type Deps struct {
	RepoSettings   *postgres.RepoSettingsStore
	ReviewVerdicts *postgres.ReviewVerdictStore
}
