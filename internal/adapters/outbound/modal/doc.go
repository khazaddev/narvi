// Package modal implements the Modal SandboxProvider adapter (§4.1):
// internal/app/ports.SandboxProvider backed by a real *http.Client talking
// to Modal's sandbox-management API.
//
// Modal is Narvi's snapshot-based provider (§3.2: "stopped|stale +
// snapshot -> restore (new gen)") — it does NOT support persistent resume
// of the same underlying instance (a future provider, e.g. RWX at Step
// 48, is expected to). Capabilities() reports that split explicitly:
// Snapshots/ImageBuilds/ExplicitStop true, Resume false; ResumeSandbox
// itself returns a permanent ports.ProviderError rather than attempting
// an operation Modal does not support.
//
// There is no real Modal account/API reachable from this codebase's tests
// or CI. Every wire-shape decision here (endpoint paths, JSON bodies) is
// this adapter's own, clearly-documented invention, exercised against a
// fake httptest.Server standing in for Modal's real API — NOT against
// real Modal API docs. The exact wire contract gets pinned for real
// whenever the live Modal integration is first wired up; what THIS Step
// proves is every externally observable behavior that matters regardless
// of the exact wire shape:
//
//   - The SESSION_CONFIG document travels as one opaque JSON blob (§4.1:
//     "the provider never assembles env fragments") — never reassembled
//     from separate fragments.
//   - Auth is attached to every request (Authorization: Bearer <token>).
//   - The correlation id (§5.3) propagates as a header when present in
//     the request context, and is omitted when absent.
//   - Responses are parsed and classified by HTTP status class — never by
//     string-matching a human-readable error message (§4.1).
//   - The HTTP client's timeout is wired from
//     platform.Timeouts.ProviderHTTPClientTimeout, which must exceed
//     platform.Timeouts.ProviderWorstColdStart (§4.1).
//   - The optional egress proxy (§4.1: "All Modal traffic goes through
//     the configurable egress proxy") is honored when configured, and
//     bypassed (direct connection) when not.
//   - BuildImage's optional build-time dependency cache (§19.1's closing
//     paragraph, Step 43(c), ports.ImageSpec.CacheMount) is a PURE
//     ACCELERATOR: a request carrying no CacheMount is byte-for-byte
//     unaffected, and cache trouble reported by the (invented) wire
//     protocol — including a transport-level hang and an unparseable
//     response, not only a handful of structured error codes — degrades
//     to an ordinary cold build via one transparent retry rather than
//     ever surfacing as a BuildImage failure — see provider.go's own
//     BuildImage doc comment and errors.go's isCacheMountTrouble. The
//     mount itself is requested READ-ONLY for the build's duration, with
//     a single write-back after success (ports.CacheMount's own doc
//     comment) — not the "content-addressed, so concurrent writes can't
//     collide" argument an earlier draft of this adapter's own comments
//     made, which review found false against the real package-manager
//     caches.
package modal
