// Package provenance holds sessions.provenance_tag's own small, fixed
// vocabulary of well-known values as exported string constants, shared
// across packages that otherwise cannot import one another directly.
//
// # Why this package exists
//
// internal/adapters/inbound/httpapi already owns one provenance_tag value
// (scopedEnvironmentProvenanceTag, create.go, "scoped_environment") as an
// unexported constant, since it is the only package that ever needed to
// read or write it -- until this Step. Step 48's own sentinel-auto-fix
// flow needs a SECOND, distinct provenance_tag value ("sentinel_auto_fix")
// checked from THREE places that cannot import each other:
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
package provenance

// SentinelAutoFix is the sessions.provenance_tag value a sentinel-auto-fix
// child session (§17.2) is created with, set once, at spawn time
// (httpapi.SpawnChildSession, childsession.go) -- NEVER set on any other
// kind of session. Three independent things key off this exact value:
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
