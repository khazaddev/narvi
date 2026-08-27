// This file (resolve.go) implements Resolve -- the one function §30.8
// names as "read per call at each egress seam through the resolver
// package". See doc.go for the package-level design.

package egressmode

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/khazaddev/narvi/internal/platform"
)

// Resolve computes repoFullName's own EFFECTIVE egress mode -- §30.8's
// central formula, "platformShadow OR NOT live_egress_enabled" -- and
// returns it as a Capability. Every future egress seam calls this, and
// only this, to decide whether it may act live for one call; see doc.go's
// own "this Step is dark" section for why nothing does yet.
//
// # Why this returns no error
//
// Every other repo_settings read in this codebase that must fail closed
// returns a bare bool, never (bool, error) -- internal/app/reviewverdict.
// AutoMergeEnabled and internal/app/sessionactor/reviewretrigger.go's own
// auto-retrigger read both follow this shape already. Resolve follows the
// SAME shape for the SAME reason, deliberately: there is no second return
// value for a careless caller to discard on the way to accidentally
// observing "live" on a degraded read (`cap, _ := Resolve(...)` is not a
// sentence Go lets anyone write against this signature, because there is
// nothing after Capability to underscore away). Every failure path below
// -- a missing row, pgx.ErrNoRows, a genuine connection error, a context
// cancellation -- resolves to shadowCapability() and is returned exactly
// like a clean shadow decision; a genuine infra failure is logged at Warn
// so it stays observable, but it can never reach the caller as anything
// other than suppression. A caller that needs to distinguish "this repo
// is deliberately configured shadow" from "the read failed" needs a
// different function -- nothing about the §30.1 zero-trace guarantee is
// allowed to depend on every future call site making that distinction
// correctly.
//
// # Why PlatformShadow is checked before any Postgres read
//
// deps.PlatformShadow (NARVI_SHADOW_MODE) is a deployment-wide constant
// read once at boot -- when it is true, no per-repo Postgres value can
// change the outcome (§30.8's own formula ORs the two), so checking it
// first both skips a needless round trip and means a dedicated evaluation
// deployment (§30.10's "minimal safe subset") stays provably shadow even
// if repo_settings itself is momentarily unreachable.
//
// # Using this for the §30.8 epoch stamp
//
// See doc.go's own "using this for the §30.8 epoch stamp" section: call
// Resolve exactly once, at the moment a durable decision artifact is
// created, and persist the result on that artifact rather than calling
// Resolve again at delivery/effect time.
func Resolve(ctx context.Context, deps Deps, repoFullName string) Capability {
	if deps.PlatformShadow {
		return shadowCapability()
	}

	settings, err := deps.RepoSettings.Get(ctx, repoFullName)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			platform.Logger(ctx).Warn("egressmode: read repo settings failed -- resolving shadow (fail-closed, §30.8)", "error", err, "repo_full_name", repoFullName)
		}
		// pgx.ErrNoRows (unwrapped): no row exists yet -- the ordinary
		// "never explicitly promoted" state, not a fault, so no warning
		// is logged for it -- mirrors AutoMergeEnabled's own identical
		// "missing row is not an error condition" precedent.
		return shadowCapability()
	}

	if !settings.LiveEgressEnabled {
		return shadowCapability()
	}
	return liveCapability()
}
