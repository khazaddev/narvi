// Package servicemanifest implements the pure schema + validation logic for
// a repo's optional .narvi/services.yml (§14.2, "Multi-service boot
// manifest"): a declarative list of services a prototyping Environment
// needs supervised alongside each other (e.g. a frontend dev server plus a
// mock API), replacing the single setup.sh/start.sh contract (§6.4) for
// repos that opt in.
//
// This package does NOT read any file, and does NOT unmarshal YAML itself
// -- Validate takes a RawManifest a caller (internal/sandboxagent/services)
// already decoded (via its own yaml.Unmarshal against bytes read from
// disk), exactly like internal/domain/sandboxboot never touches an env var
// directly. Like every other internal/domain/* package (see
// internal/domain/sandboxboot's own doc.go), this package imports nothing
// outside the standard library: RawManifest/RawService/RawReadiness carry
// `yaml` struct tags purely as metadata for the impure caller's own
// yaml.Unmarshal to read via reflection -- declaring a struct tag does not
// require importing the library that later interprets it. Zero
// time.Now(), zero randomness, zero disk/network I/O.
//
// Design choices worth flagging explicitly, since the plan text
// underspecifies them:
//
//   - Readiness.Health's shape. §14.2's own YAML example only ever shows
//     `readiness: { port: 3000 }` -- it never shows a worked health-check
//     example, even though IMPLEMENTATION_PLAN.md's row summary says
//     "readiness (port/health)". This package defines Health as a single,
//     self-contained absolute URL (e.g. "http://127.0.0.1:4000/health")
//     the caller GETs directly, rather than (for instance) a bare path
//     combined with the service's own port -- a self-contained URL needs
//     no additional field to be meaningful, and mirrors how a human would
//     write a curl command against the service by hand. A 2xx status
//     means ready; anything else (including a connection failure) does
//     not.
//   - Readiness must have EXACTLY ONE of Port or Health set. Both empty
//     means "we don't know how to check readiness"; both set means "which
//     one wins" -- neither is a reasonable default, so Validate rejects both
//     shapes outright rather than picking one silently.
//   - Cwd is repo-root-relative and must not escape it: any ".." path
//     segment or an absolute path is rejected. Unlike
//     internal/domain/environment's own hasDotDotSegment (shaped for
//     gitignore-style glob patterns, including a leading "!" negation
//     sigil), this package writes its own small, unrelated check for a
//     plain relative directory path -- a different concern, not reused.
package servicemanifest
