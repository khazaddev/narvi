// Package provenance holds sessions.provenance_tag's own small, fixed
// vocabulary of well-known values as exported string constants, shared
// across packages that otherwise cannot import one another directly.
//
// # Why this package exists
//
// internal/adapters/inbound/httpapi originally owned the ONE
// provenance_tag value this package started with (scopedEnvironmentProvenanceTag,
// create.go, "scoped_environment") as an unexported constant, since it was
// the only package that ever needed to read or write it -- until Step 48
// below. §8.2's own sentinel-auto-fix flow needed a SECOND, distinct
// provenance_tag value ("sentinel_auto_fix") checked from THREE places
// that cannot import each other:
// internal/app/sessionactor (dispatch.go, to select the OpenCode
// capability-restricted agent; pushpr.go, to route a fix session's own PR
// creation through the amendment-mandated bypass of resolvePRBaseBranch,
// never the ordinary per-repo loop) cannot import internal/adapters/
// inbound/httpapi (the package that spawns a sentinel-fix child session)
// -- CLAUDE.md's own layering rule, and §24.3 (TECHNICAL_PLAN.md:846)
// already documents this exact constraint for a structurally identical
// problem. A tiny, dependency-free domain package below both is the
// correct home, mirroring internal/domain/reposource's own "shared,
// dependency-free vocabulary" precedent.
//
// This package is deliberately NOT internal/domain/session (that package
// is pure status-DERIVATION logic, §3.1 -- a plain string constant naming
// convention is a different kind of thing, and does not belong mixed into
// it) and NOT internal/domain/environment (provenance_tag values are not
// all environment-scoping-related -- SentinelAutoFix has nothing to do
// with path-scoping/mock-config).
//
// §14.4 ("handoff-readiness sentinel", §14.4) adds ScopedEnvironment
// below -- the SAME "scoped_environment" value httpapi/create.go already
// wrote under its own private name, now promoted here (create.go's own
// constant is retired in favor of this one, never left as a second,
// independently-maintained copy of the same string) because a FOURTH
// place now needs to read it and cannot import httpapi:
// internal/app/sessionactor's own handoff-sentinel orchestration
// (handoffsentinel.go), invoked from pushpr.go's createPRBestEffort right
// after a scoped session's PR is created -- httpapi already cannot be
// imported from sessionactor (github/coalesce.go's own identical
// import-cycle constraint, SentinelAutoFix's doc comment above), and
// sessionactor is exactly where §14.1 says this check belongs: "sessions
// created under a scoped Environment carry a provenance tag... so the
// label automation and the handoff sentinel (§14.4) can act on it without
// re-deriving intent."
package provenance

// ScopedEnvironment is the sessions.provenance_tag value a session created
// under a path_scope'd Environment carries (§14.1) -- set once, at
// session-creation time, whenever environment.RequiresProvenanceTag
// reports true for that Environment (httpapi/create.go's own
// buildSessionInsertParams). Two independent things key off this exact
// value:
//
//  1. §14.1's own label-automation intent (not built by any Step so far).
//  2. §14.4's own handoff-readiness sentinel (§14.4): sessionactor's own
//     runHandoffSentinelBestEffort (handoffsentinel.go) checks this tag on
//     the session that just created a PR before doing ANY further work --
//     an ordinary (nil-tagged) session's PR is completely untouched, no
//     extra API calls, no label, no comment.
const ScopedEnvironment = "scoped_environment"

// IsScopedEnvironment reports whether tag (a session's own, possibly-nil
// sessions.provenance_tag, sqlcgen.Session.ProvenanceTag's own *string
// shape) names a session created under a scoped (path_scope'd)
// Environment. A nil tag (every ordinary, unscoped session) is never
// mistaken for one -- mirrors IsSentinelAutoFix's own identical
// discipline below.
func IsScopedEnvironment(tag *string) bool {
	return tag != nil && *tag == ScopedEnvironment
}

// SentinelAutoFix is the sessions.provenance_tag value a sentinel-auto-fix
// child session (§17.2) is created with, set once, at spawn time (via
// httpapi.ChildSessionOptions.ProvenanceTag on a CreateSessionOnTx call --
// internal/app/outboxworker's own sentinelAutoFixNotifier,
// sentinelautofix.go) -- NEVER set on any other kind of session. Three
// independent things key off this exact value:
//
//  1. §17.1's own "no recursion" rule: a review verdict posted on a PR
//     whose OWN session carries this tag never itself triggers ANOTHER
//     sentinel auto-fix, regardless of what its verdict finds (reviewverdict.go).
//  2. §17.2's own OpenCode capability restriction: dispatch.go's
//     buildPromptPayload sets sandboxws.Prompt.CapabilityRestricted true
//     for exactly this tag, selecting the glob-restricted "sentinel-fix"
//     OpenCode agent (internal/adapters/outbound/opencode) instead of the
//     ordinary "build" agent.
//  3. §17.2's own base-branch-bypass amendment: pushpr.go's
//     createPRBestEffort routes a session carrying this tag to
//     createSentinelFixPRBestEffort instead of the ordinary per-repo
//     resolvePRBaseBranch loop.
const SentinelAutoFix = "sentinel_auto_fix"

// IsSentinelAutoFix reports whether tag (a session's own, possibly-nil
// sessions.provenance_tag, sqlcgen.Session.ProvenanceTag's own *string
// shape) names a sentinel-auto-fix child session. A nil tag (every
// ordinary session) is never mistaken for one.
func IsSentinelAutoFix(tag *string) bool {
	return tag != nil && *tag == SentinelAutoFix
}
