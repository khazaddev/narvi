// This file (resolve.go) implements Resolve -- the one function §30.8
// names as "read per call at each egress seam through the resolver
// package". See doc.go for the package-level design.

package egressmode

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/narvidev/narvi/internal/platform"
)

// Resolve computes repoFullName's own EFFECTIVE egress mode -- §30.8's
// central formula, "platformShadow OR NOT live_egress_enabled" -- and
// returns it as a Capability. Every future egress seam calls this, and
// only this, to decide whether it may act live for one call; see doc.go's
// own "this Step is dark" section for why nothing does yet.
//
// # Why this returns no error
//
// The two reads §30.8 names as the precedent for this polarity --
// internal/app/reviewverdict.AutoMergeEnabled and internal/app/
// sessionactor/reviewretrigger.go's auto-retrigger read -- both return a
// bare bool rather than (bool, error), and Resolve follows them for the
// same reason. This is NOT a claim about every fail-closed read in the
// codebase: others do return an error, and the shape is a judgement about
// what a caller can do wrong here, not a house rule. What makes it right
// here specifically: there is no second return
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

// ResolvePlatform reports the effective Capability for a durable artifact
// that has no single customer repository to check §30.8's per-repo flag
// against -- an outbox row whose owning session names no single repo (a
// multi-repo session, or one with none at all), or any other decision
// artifact where the per-repo axis simply does not apply. With no repo
// identified, there is nothing for repo_settings.live_egress_enabled to
// say one way or the other, so the only question that CAN still be asked
// is whether this whole deployment is itself a shadow evaluation
// deployment (platformShadow, NARVI_SHADOW_MODE) -- exactly the first
// half of Resolve's own formula, never re-implemented here.
//
// This is deliberately NOT "when in doubt, shadow": a repo-less/
// multi-repo notification on an ordinary, non-shadow deployment (every
// existing repo, today, before this Step) must keep behaving exactly as
// it always has -- Resolve's own per-repo fail-closed default
// (repo_settings.live_egress_enabled defaults to false, migrations/
// 000101_repo_settings_live_egress_enabled.up.sql) exists to make a
// NEWLY-CONNECTED repo start safe, not to retroactively silence every
// notification this codebase already sends whenever it cannot name one
// specific repo to check that column against.
func ResolvePlatform(platformShadow bool) Capability {
	if platformShadow {
		return shadowCapability()
	}
	return liveCapability()
}
