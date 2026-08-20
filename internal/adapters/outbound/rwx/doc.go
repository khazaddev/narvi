// Package rwx implements the RWX (rwx.com) SandboxProvider adapter and PR
// preview links (§4.1.1/§4.1.2) — internal/app/ports.
// SandboxProvider's SECOND real implementation, alongside
// internal/adapters/outbound/modal, integrating RWX's real, public product
// exactly as modal integrates Modal's API and githubapi integrates
// GitHub's. Everything in this package is grounded in RWX's own published
// documentation (www.rwx.com/docs, fetched 2026-08-06); where that
// documentation is silent, the gap is named in this package's own doc
// comments (and §4.1.3) rather than papered over with an invented API
// shape.
//
// # Two transports, two RWX primitives
//
//  1. Sandbox lifecycle (Provider, provider.go): RWX documents no HTTP API
//     for sandbox lifecycle — its programmatic sandbox surface is the
//     pinned `rwx` CLI (global `--format json`), driven as a subprocess
//     via runner.go's cliRunner seam (the real execCLIRunner in
//     production; a fake in every test in this package other than the
//     one real-binary contract stub, realbinary_test.go). RWX_ACCESS_TOKEN
//     travels to the subprocess as an environment variable (RWX's own
//     documented mechanism), never as CLI argv — argv is visible to
//     process listings (§5.2's leak-class discipline), the exact safety
//     property internal/adapters/outbound/modal's own Authorization-header
//     credential threading already gets for free from using HTTP.
//  2. Preview-app dispatch (DispatchClient, previewnotifier.go): RWX's one
//     documented HTTP API — POST https://cloud.rwx.com/mint/api/runs/
//     dispatches, polled via GET .../dispatches/:id — used ONLY by the PR
//     preview mechanism (§4.1.2), never by sandbox lifecycle. This is a
//     plain, real REST call this package tests for real against a fake
//     httptest.Server, unlike the CLI-dependent sandbox-lifecycle surface.
//
// # What is genuinely untested here, and why
//
// There is no real `rwx` CLI binary or RWX_ACCESS_TOKEN reachable from
// this codebase's own tests or CI — a deliberate, named, user-approved
// scope decision for this Step (see the landing PR's own description).
// Every CLI arg/env/JSON-envelope shape this package builds (config.go,
// wire.go, errors.go) is this adapter's own clearly-documented invention
// — exercised against a fake cliRunner standing in for the real binary,
// mirroring internal/adapters/outbound/modal's own openly-admitted
// invented wire shapes (modal/doc.go: exercised against a fake httptest.
// Server, "NOT against real Modal API docs") — the same precedent, one
// transport layer over (a fake exec.Cmd round trip instead of a fake HTTP
// round trip). realbinary_test.go carries exactly one skipped test
// function naming precisely what a real pinned `rwx` CLI + RWX_ACCESS_TOKEN
// would let it verify.
//
// What THIS package proves regardless of the exact CLI wire shape:
//
//   - RWX_ACCESS_TOKEN reaches the subprocess via its environment, never
//     its argv.
//   - SESSION_CONFIG travels as one opaque JSON value, never spread env
//     fragments (§4.1: "the provider never assembles env fragments").
//   - A sandbox's identity (its generated config path) embeds BOTH the
//     session id and the gen, so two gens can never collide onto one RWX
//     sandbox (§3.2 fencing at the provider's own identity layer).
//   - `--format json` is passed on every CLI invocation.
//   - CLI failures are classified by process exit code (+ the decoded
//     `--format json` error envelope) — never by string-matching stderr
//     text (§4.1's own rule).
//   - Dispatches-API failures are classified by HTTP status class,
//     mirroring modal's own table (§3.2's "unknown defaults to transient"
//     rule applies identically here).
//
// # Capabilities — declared from what RWX verifiably supports
//
// Capabilities() reports Snapshots: false, ExplicitStop: true,
// ImageBuilds: false, and — the one flag genuinely unresolved without real
// RWX access — Resume: false, the conservative default until stop→start
// state preservation is verified empirically against a real account
// (§4.1.3 names this as Step 57's own first exit criterion; see
// Provider.Capabilities' own doc comment, provider.go, for the full
// reasoning). TakeSnapshot/RestoreFromSnapshot/BuildImage/DeleteImage/
// ResumeSandbox each return the permanent UNSUPPORTED_OPERATION
// ProviderError, mirroring modal.Provider.ResumeSandbox's own established
// pattern for an operation a provider's Capabilities() already reports as
// unsupported.
package rwx
