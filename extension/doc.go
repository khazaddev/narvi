// Package extension is Narvi's module-facing façade: the one non-
// internal package (besides controlplane itself) a private module
// composed on top of this repository is allowed to import. Go's own
// `internal` rule means a separate module can never import anything
// under github.com/narvidev/narvi/internal/... (see this repository's
// own docs/design/boundaries-design.md, section 0) -- so every type a composed
// module needs to see is re-exported here, through type aliases wherever
// the identity of an existing internal type must be preserved (a private
// implementation of an aliased interface satisfies the internal one
// unmodified), or through a small purpose-built struct where no internal
// equivalent exists at all (Module, Runtime).
//
// This package is deliberately a LEAF: it may import internal/... freely
// (transitive imports are legal; the internal rule is about the
// importing file's OWN import paths, never what a dependency imports),
// but nothing under internal/... may ever import this package back, and
// the only other package allowed to import it is controlplane, the
// composition root. tools/lint/narvichecks/capabilityimportban enforces
// the capability-specific half of that rule structurally: almost nothing
// in this codebase may import this package, internal/app/capability, or
// internal/domain/license at all, because a capability decision is a
// system-boundary decision that must never leak onto a shadow-mode
// suppression path (technical plan §30, §34.5).
//
// Growing the crossing set this package represents -- a new field, a new
// alias -- is a deliberate, reviewable PR in this repository, never
// something a private module can silently widen from its own side.
package extension
