// Package providercredential implements Step 53's own ("provider
// credential injection", §25.1/§25.3) pure domain vocabulary: which
// providers/scopes this codebase's first secret-storage table
// (provider_credentials, migrations/000056_provider_credentials.up.sql)
// recognizes, which OpenCode env-var name(s) each provider maps onto, and
// the "most specific wins" resolution rule that picks exactly one
// candidate row out of however many can ever apply to one (provider,
// session) pair. That count is NOT a fixed 3: a session names a LIST of
// repos (sessions.repos, primary repo always at position 0, §3.4), and
// migration 000056's own unique index is scoped per-repo
// ((scope, scope_target_id, provider) WHERE scope_target_id IS NOT NULL)
// -- nothing prevents every repo in an N-repo session from each having its
// own repo-scoped row for the same provider. The real bound is N+2 for an
// N-repo session: N repo-scoped candidates (one per named repo, at most),
// plus at most 1 environment-scoped and at most 1 global-scoped candidate
// (both pinned to a single scope_target_id -- this session's own
// environment_id, or NULL -- so neither can ever have more than one row
// per provider). 3 is only the N=1 case.
//
// No I/O, no time.Now(), no randomness anywhere in this package (CLAUDE.md
// §11) -- Resolve (resolve.go) is handed an already-fetched, already-
// decrypted (or still-opaque -- see its own doc comment) candidate slice
// by its caller (internal/adapters/inbound/httpapi's own sandbox-facing
// delivery endpoint does the actual Postgres read + platform.DecryptToken
// call), and simply picks a winner.
//
// # Resolution order: verified against the actual spec text, not assumed
//
// §25.3's own Step 53 brief describes this as "repo -> environment ->
// global, most specific wins" -- but two independent places in this
// codebase's own already-written documentation say otherwise, and this
// package follows THEM, not that paraphrase (this repo's own CLAUDE.md:
// resolve ambiguity against the actual spec text, never a summary of it):
//
//   - docs/TECHNICAL_PLAN.md §12.2 item 5 (Settings view mockup): "Secrets:
//     table with scope chips and per-target resolution display (order:
//     automation -> environment -> repo -> global, "this value wins")."
//   - internal/domain/automation/doc.go's own "per-automation secrets:
//     deferred to Step 53" section: "...alongside the repo/environment/
//     global scopes mockups.html's own Settings view already shows
//     resolving in that order ("automation -> environment -> repo ->
//     global, this value wins")."
//
// Both independently give the SAME order, automation first (most
// specific) down to global (least specific) -- automation was out of
// scope for Step 53 itself (§8.4's own "per-automation secrets"
// deferral was for a LATER, focused follow-up, not provider_credentials),
// so for the 3 scopes THAT Step actually built, the real, doubly-confirmed
// precedence was:
//
//	environment  (most specific)
//	repo
//	global       (least specific, always exists as a fallback)
//
// i.e. an environment-scoped credential shadows a repo-scoped one for the
// SAME provider, which in turn shadows the global one. This is the
// opposite of "repo before environment" -- a genuine, verified correction
// against the Step brief's own paraphrase, not a guess; see that Step's
// own PR description for the same note.
//
// # Step 72 (§27.1): this package's own Scope/Resolve now serve a SECOND
// table
//
// sandbox_secrets (migrations/000090_sandbox_secrets.up.sql) is a
// deliberately SEPARATE table from provider_credentials (see that
// migration's own top comment for why a second table, not a widened
// ENUM), but it resolves through THIS package's Scope type and Resolve
// function, unchanged, per §27.1's own explicit instruction: "resolution
// automation -> environment -> repo -> global via the SAME generic
// providercredential.Resolve... only the scopePriority table gains the
// fourth row." That fourth row is ScopeAutomation (scope.go) -- the most
// specific of sandbox_secrets' own 4 scopes, confirmed by the SAME two
// sources this file already cites above (§12.2 item 5's Settings mockup
// and automation/doc.go both independently give "automation ->
// environment -> repo -> global", automation first) for the ADDITIONAL
// automation level neither source needed to justify back when this file
// was written for Step 53 alone.
//
// Two consequences worth stating plainly, since they are easy to miss
// reading this package in isolation:
//
//  1. This package's own name ("providercredential") is now narrower than
//     its own charter -- Scope/Resolve are genuinely provider-agnostic
//     generic vocabulary, reused by a table that has nothing to do with
//     LLM provider credentials at all. This was a deliberate choice (the
//     Step 72 brief's own instruction, not a drift this package's own
//     author introduced unprompted): a new, differently-named package for
//     4 lines of generic scope-priority logic would fork the ONE piece
//     already built specifically to be reused (Candidate[T]/Resolve's own
//     doc comment: "generic... specifically so this package never has to
//     know or care whether the caller has already decrypted..."). Renaming
//     this package is left as a possible future cleanup, not done here,
//     to keep §27.1's diff to the addition it actually is.
//  2. ScopeUser (Step 59, provider_credentials only) and ScopeAutomation
//     (Step 72, sandbox_secrets only) are two DIFFERENT tables' own most-
//     specific level, sharing this one priority map purely for
//     convenience -- see Scope's own doc comment for why the two never
//     actually compete inside a single Resolve call.
package providercredential
