// Package reviewverdict is the app-layer half of §21.1/§21.2's own
// verdict-persistence, analytics, and auto-approval-config read model --
// it aggregates Postgres (never itself I/O-free, unlike its sibling pure
// package internal/domain/reviewverdict, which this package calls but
// never duplicates the reduction logic of) and converts between the
// wire/storage shapes (sqlcgen rows, JSONB bytes) and the pure domain
// shapes (review.Verdict, reviewverdict.Record, autoapproval.
// EligibilityConfig) at exactly one seam, mirroring internal/app/
// decisioninbox's own identical split between "pure decision functions
// in internal/domain" and "Postgres aggregation here".
package reviewverdict

import (
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/platform"
)

// Deps bundles every dependency this package's own functions need --
// constructed once at process wiring time (cmd/control-plane/main.go),
// mirroring every other app-layer Deps struct in this codebase.
type Deps struct {
	ReviewVerdicts       *postgres.ReviewVerdictStore
	RepoSettings         *postgres.RepoSettingsStore
	ReviewFindings       *postgres.ReviewFindingStore
	AutoApprovalOutcomes *postgres.AutoApprovalOutcomeStore

	Timeouts platform.Timeouts
}
