// Package license is Narvi's pure, offline licence-grant domain: decoding
// and verifying a signed capability grant against an embedded Ed25519
// keyset. Stdlib only -- no I/O, no time.Now() (every temporal check
// takes `now` as an explicit parameter, per this repository's own
// /internal/domain convention, technical plan §11), no randomness. See
// technical plan §34.5 and docs/design/boundaries-design.md, section 1, for the
// full design this package implements: a capability is enabled only when
// it is installed (a composed module supplies it), licensed (a verified
// grant names it) and valid now (the grant's own [Grant.ValidAt] window
// contains the current instant) -- that conjunction lives one layer up,
// in internal/app/capability, which is this package's only production
// caller besides the composition root (controlplane) and the extension
// façade.
//
// # The v1 capability vocabulary is not fixed yet
//
// [CapabilityGovernance] and [CapabilityKnowledgeRetrieval] are
// PLACEHOLDERS pending a product decision -- see
// docs/design/boundaries-design.md, section 7 ("left to the repository owner:
// the v1 capability vocabulary"). Nothing in this repository puts either
// name on the wire in this PR: there is no contracts/rest/v1/
// dtos.schema.json entry for either constant, and no REST handler reads
// this package at all yet (GET /api/capabilities is later, separate
// work). That is deliberate, not an oversight -- once a capability name
// crosses into a published wire contract, renaming it becomes a breaking
// change (technical plan §34.4); until then, renaming either constant is
// a private, self-contained edit to this package and its two callers
// (internal/app/capability, extension), never a contract migration.
// Treat every reference to either name in this codebase as provisional
// until that contract PR lands.
//
// # Key custody
//
// The signing seed that produces a valid licence key never exists in
// this repository -- see [IssuerKeys]'s own doc comment. This package can
// verify a signature; it cannot produce one, by design (see the design
// note's own "the signing tool and its key custody are the private
// repo's").
package license
