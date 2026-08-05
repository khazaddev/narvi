// Package providercredential implements Step 53's own ("provider
// credential injection", §25.1/§25.3) pure domain vocabulary: which
// providers/scopes this codebase's first secret-storage table
// (provider_credentials, migrations/000056_provider_credentials.up.sql)
// recognizes, which OpenCode env-var name(s) each provider maps onto, and
// the "most specific wins" resolution rule that picks exactly one
// candidate row out of the (at most 3) that can ever apply to one
// (provider, session) pair.
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
// specific) down to global (least specific) -- automation is out of
// scope for this Step (§8.4/Step 52's own "per-automation secrets"
// deferral is for a LATER, focused follow-up, not this table), so for the
// 3 scopes this Step actually builds, the real, doubly-confirmed
// precedence is:
//
//	environment  (most specific)
//	repo
//	global       (least specific, always exists as a fallback)
//
// i.e. an environment-scoped credential shadows a repo-scoped one for the
// SAME provider, which in turn shadows the global one. This is the
// opposite of "repo before environment" -- a genuine, verified correction
// against the Step brief's own paraphrase, not a guess; see this Step's
// own PR description for the same note.
package providercredential
