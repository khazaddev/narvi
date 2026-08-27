// This file (deps.go) defines Resolve's own two inputs.

package egressmode

import (
	"context"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// RepoSettingsReader is the narrow slice of *postgres.RepoSettingsStore
// Resolve actually needs: one read, keyed by repoFullName.
//
// This is an interface, unlike most app-layer Deps structs in this
// codebase (e.g. internal/app/reviewverdict.Deps.RepoSettings, which
// holds the concrete *postgres.RepoSettingsStore directly) -- deliberately,
// for exactly one reason: §30.8 requires a DEDICATED test pinning Resolve
// to "suppress" on pgx.ErrNoRows and on an arbitrary error, and a fake
// satisfying this interface can return either canned outcome without a
// real Postgres. The concrete *postgres.RepoSettingsStore also satisfies
// this interface unmodified (structural typing, no adapter needed) and is
// what production wiring passes; it is also what
// resolve_integration_test.go uses for the THIRD pinned case, "an absent
// row" -- deliberately NOT reused as a fake for that case, since the risk
// this whole package exists to guard against is a resolver that only
// behaves correctly against a hand-authored pgx.ErrNoRows value and
// silently diverges from what a REAL missing row produces.
type RepoSettingsReader interface {
	Get(ctx context.Context, repoFullName string) (sqlcgen.RepoSetting, error)
}

// Deps bundles Resolve's own two inputs -- constructed once at process
// wiring time, mirroring every other app-layer Deps struct in this
// codebase.
type Deps struct {
	// RepoSettings reads the per-repo repo_settings.live_egress_enabled
	// authority (§30.8).
	RepoSettings RepoSettingsReader

	// PlatformShadow is platform.Config.ShadowMode (NARVI_SHADOW_MODE,
	// §30.8's own deployment-level master switch), read ONCE at process
	// boot and passed straight through here -- never re-read from the
	// environment per call. See Resolve's own doc comment for why this
	// is checked before RepoSettings is ever consulted.
	PlatformShadow bool
}
