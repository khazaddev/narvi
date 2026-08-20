// Package webui is the inbound adapter that serves the web UI's static
// build (§12.1: "SPA, no SSR, no BFF... Static build embedded in the
// control-plane binary via go:embed; narvi serve serves API + WS + UI on
// one port") -- the sibling of httpapi (REST) and wshub (WS) for this one
// remaining surface: everything that is neither.
//
// # The trap this package exists to structurally prevent
//
// This binary already serves 6 top-level route groups with no SPA involved
// at all: /.well-known/* (§27.3's public, UNAUTHENTICATED OIDC discovery +
// JWKS documents -- the trust anchor customer clouds federate to, fetched
// directly by AWS/GCP/Azure's own STS with no Narvi credential), /api/*,
// /auth/*, /health, /sessions/*, and /webhooks/*. An SPA fallback mounted
// the naive way -- a hand-rolled "is this an API-ish path" prefix check
// that must independently enumerate all 6 groups correctly, or a wildcard
// route registered in a way that happens to win a routing-priority race --
// is exactly one missed prefix or one reordering away from swallowing one
// of those into a 200 index.html response. For /.well-known/* specifically
// that failure is silent on THIS side: the control plane logs a healthy
// 200, and every cloud federation attempt just starts failing on the
// customer's side with an error this codebase never observes (mirrors
// oidcdiscovery.go's own "gap-3" silent-failure precedent).
//
// Mount (mount.go) is built to make that structurally impossible rather
// than a matter of getting an enumeration right: it wires the SPA
// exclusively through chi's own r.NotFound(...) hook, which by chi's own
// routing contract fires ONLY when no registered route matched at all --
// every route main.go registers under the 6 groups above (present now or
// added later) is tried FIRST, unconditionally, regardless of where in
// main.go Mount is called from. isProtectedPath is a second, independent
// belt-and-suspenders layer on top of that structural guarantee: even a
// request path that LOOKS like it belongs to one of the 6 groups but
// matches no real registered route (a typo, a removed route, a route not
// yet added) gets a real 404 from Mount itself, never a 200 SPA shell --
// see mount_test.go's own TestMount table for both layers pinned together,
// and its own doc comment for the mutation that proves the guard is
// load-bearing, not decorative.
//
// # The go:embed placeholder problem, and the fix chosen
//
// The go:embed directive requires its target directory to exist, with at
// least one matching file, AT COMPILE TIME -- so `//go:embed all:dist` would be a
// compile error on any checkout that has never run `make web-build` (a
// fresh clone, most CI jobs other than the dedicated frontend one, any Go-
// only contributor). "The binary must still build and run when the assets
// have not been built" (this Step's own requirement) rules that out
// unconditionally.
//
// Three fixes were available: commit a placeholder file the embed target
// always contains; a build tag; or an `all:` pattern over a checked-in
// stub. This package uses a BUILD TAG (assets_stub.go / assets_embed.go,
// selected by the `web_assets` tag), not a checked-in placeholder, because
// the placeholder approaches all share one real cost a build tag avoids
// entirely: this package's own dist/ subdirectory has to be BOTH a real,
// gitignored Vite build output directory (so a contributor's
// `make web-build` isn't fighting git over files it just regenerated) AND
// contain a tracked file for go:embed to find on a pristine checkout --
// those two requirements collide the moment Vite's own default
// `emptyOutDir` behavior deletes that tracked file during a real build,
// leaving it permanently "deleted" in `git status` until someone remembers
// to restore it by hand. A build tag needs no on-disk directory at all for
// the default (no-tag) build: assets_stub.go carries no go:embed
// directive, so `go build ./...` compiles identically whether or not
// dist/ exists on disk, and DistFS is simply nil (a legal fs.FS zero value
// Mount's own doc comment on that parameter describes) -- Mount serves a
// small in-Go placeholder message for every SPA route instead of a
// missing file. Building the real, embedded frontend is
// `go build -tags web_assets ./cmd/control-plane`, which DOES require
// dist/ to actually exist (i.e. `make web-build` first, which points
// Vite's own build output directly at it -- see web/vite.config.ts's own
// comment for why) -- the single-binary `make dist` recipe this unlocks
// (§12.4: "make dist produces the single self-contained binary") is a
// later Step's own scope, not built here.
package webui
