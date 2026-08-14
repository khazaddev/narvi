package imagebuild

import (
	"crypto/sha256"
	"encoding/hex"
)

// WellKnownCachePaths is the fixed, closed, package-manager-agnostic set of
// cache directories §19.1's own "build-time dependency cache" (Step 43(c))
// mounts a persistent, provider-backed cache volume at, inside the build
// sandbox — mirroring §19.1 item 4's own "fixed, closed set" discipline for
// the dependency-manifest scan (the SAME idiom, applied to a different
// question: WHERE a package manager caches downloaded content, not WHICH
// files declare what it depends on).
//
// Deliberately package-manager-agnostic: this codebase has no way to know,
// ahead of a build, which ecosystem(s) a given repo set actually uses (the
// same reasoning §19.1 item 4 and §19.6 already apply to the lockfile scan)
// — so every path below is always mounted, for every build, regardless of
// what the repo set turns out to need. An unused path costs nothing (an
// empty, never-touched directory on a shared volume); the alternative —
// detecting ecosystems up front and mounting selectively — would be exactly
// the kind of second, guessing decision path this system avoids everywhere
// else (§19.4's own "Narvi cannot know which files are setup-relevant for
// arbitrary repos, and guessing... would be exactly the kind of second,
// magical decision path this system avoids everywhere else").
//
// # Mounted read-only for the build's duration — never as a claim about content-addressing
//
// An earlier draft of this design mounted these paths READ-WRITE and
// justified skipping a lock on the grounds that every path here is a
// package manager's own "content-addressed" cache, so concurrent writers
// could only ever produce identical bytes at the identical name. That
// premise was checked against each package manager's own real, documented
// on-disk layout and found false for nearly every path below: Go's module
// cache ships its OWN lock file (`cache/lock`) and mutable `@v/list`
// version listings precisely because concurrent access is NOT safe without
// coordination; Gradle's `caches/modules-2` is binary metadata mutated in
// place, guarded by its own `modules-2.lock`/`journal-1.lock`; Cargo
// guards its registry with `.package-cache` (not even mounted here); npm's
// own `_cacache/index-v5` entries are keyed by the hash of the REQUEST URL,
// not the response content, and are written by APPEND, not atomic rename;
// pip's cache key is the SHA-224 of the request URL, and its `wheels/`
// subtree holds locally built artifacts; Composer, Bundler, and Yarn are
// similar. Worse, mounting a directory that carries a tool's own
// host-local lock file across sandboxes on DIFFERENT hosts hands that tool
// a FALSE sense of mutual exclusion — an advisory lock is invisible across
// hosts by construction.
//
// The fix is not a smarter lock, it is removing the thing a lock would
// have to guard: every path below is mounted READ-ONLY for the entire
// duration of a build, so nothing can write into the SHARED, persistent
// copy while a build is in flight — there is no interleaving to reason
// about, and a tool's own host-local advisory lock becomes irrelevant
// because there is nothing left for it to guard. Exactly one write-back,
// merging whatever this build newly produced, happens after that build
// has itself succeeded (CacheVolumeKey's own doc comment and
// ports.CacheMount have the full contract) — never before, and never for
// a build that failed. A package manager that needs to WRITE during the
// build (nearly all of them do, at least for a newly-resolved package) is
// given a private, per-build writable layer at these same logical paths —
// an ordinary read-through/write-back cache shape, whose exact mechanism
// (a copy-on-write overlay, a seeded scratch copy, ...) is the adapter's
// or build service's own concern, external to this repository, exactly as
// the mount itself and the dependency install already are (ports.
// CacheMount's own doc comment). This is what makes "read-only" compatible
// with a tool like Go's module cache that is not documented to tolerate a
// literal, unassisted read-only cache directory on its own — see that
// struct's own doc comment for the per-tool read-only-cache posture this
// design could actually verify, and the one tool (Go's own GOMODCACHE) it
// could not confirm degrades gracefully without that writable layer.
//
// Every path is a home-relative cache directory at its package manager's
// OWN documented default, assuming the build sandbox runs as root — the
// ordinary convention for a from-scratch build container ($HOME=/root). A
// repo whose build environment overrides that default (a non-root build
// user, an explicit env override redirecting the cache elsewhere) simply
// gets no benefit from the corresponding path being mounted, exactly as
// harmlessly as an unused ecosystem's own path above — never a build
// failure, just a smaller accelerator win for that one repo. Like every
// other concrete wire-level decision this codebase's own Modal build
// integration makes (internal/adapters/outbound/modal/doc.go: "no real
// Modal account/API reachable... this adapter's own invention"), these
// exact paths are this codebase's own choice, not a vendor-verified
// contract — extend this list, don't invent a second one, if a real
// deployment finds a package manager missing.
//
//   - npm's own package/metadata cache (`_cacache`) — index entries keyed
//     by request URL, written by append (not atomic rename); npm is not
//     documented to tolerate a literal read-only `--cache` directory on
//     its own (it writes into `_cacache/tmp` even on a cache MISS as part
//     of an ordinary fetch), so this is one of the paths that most needs
//     the writable layer described above rather than a bare read-only
//     bind mount.
//   - Yarn Classic/Berry's global cache. Yarn Berry documents an explicit
//     `--immutable-cache` mode (fail rather than silently drift) — the
//     closest thing to a native "read-only" posture among these tools —
//     but that is a strictness knob, not a not-fail guarantee on its own;
//     this design still relies on the writable layer, not on that flag.
//   - pnpm's own package store.
//   - pip's HTTP cache — pip documents an explicit `--no-cache-dir` flag
//     that disables caching outright and fetches normally, the cleanest
//     "explicit read-only-adjacent mode, never fails" example among these
//     eleven paths.
//   - Go's module cache, GOMODCACHE's own default location — ships its
//     OWN `cache/lock` file and mutable `@v/list`/per-module `.lock`
//     files (this design's own worked counter-example above); the ONE
//     path in this list this design could NOT confirm degrades gracefully
//     without the writable layer — see ports.CacheMount's own doc comment
//     and this design's own report for why the writable layer, not
//     tool-native degradation, is load-bearing here.
//   - Go's build cache, GOCACHE's own default location — distinct from the
//     module cache above; Go documents an explicit `GOCACHE=off` mode
//     (disables the build cache outright, compiles fresh) as its own
//     graceful degrade path.
//   - Cargo's downloaded-crate registry cache — Cargo's own advisory lock
//     (`.package-cache`) is deliberately NOT mounted here (it lives
//     alongside, not under, `registry`), so a build never inherits a
//     stale host-local lock from a different host.
//   - Composer's (PHP) package cache.
//   - Bundler's (Ruby) gem cache.
//   - Maven's local repository cache.
//   - Gradle's dependency cache — `caches/modules-2` is binary metadata
//     mutated in place, guarded by Gradle's own `modules-2.lock`/
//     `journal-1.lock` living INSIDE this same mounted directory.
//
// wellKnownCachePaths is unexported: WellKnownCachePaths() below is the
// only way to reach it, returning a fresh copy on every call so no caller
// can mutate the shared backing array a naked exported slice would expose
// (an audit-remediation finding on Step 43(c): "WellKnownCachePaths is an
// exported mutable slice handed across the port on every build").
var wellKnownCachePaths = []string{
	"/root/.npm/_cacache",
	"/root/.cache/yarn",
	"/root/.local/share/pnpm/store",
	"/root/.cache/pip",
	"/root/go/pkg/mod",
	"/root/.cache/go-build",
	"/root/.cargo/registry",
	"/root/.cache/composer",
	"/root/.bundle/cache",
	"/root/.m2/repository",
	"/root/.gradle/caches",
}

// WellKnownCachePaths returns the fixed, closed set of cache directories
// documented on the package-level var above. Every call returns a FRESH
// copy — never the shared backing array — so a caller (app/imagebuild.
// Builder populates a brand-new ports.CacheMount.Paths from this on every
// single build) can never mutate the slice one build's own ports.CacheMount
// received and have that mutation silently leak into every OTHER build's
// own CacheMount, which handing out the same backing array on every call
// would allow.
func WellKnownCachePaths() []string {
	paths := make([]string, len(wellKnownCachePaths))
	copy(paths, wellKnownCachePaths)
	return paths
}

// CacheVolumeKey deterministically names the ONE persistent cache volume a
// build against (base, runtimeVersion, epoch) should mount (§19.1's own
// closing paragraph: "keyed on Base + RuntimeVersion... plus an explicit
// rotation epoch — never on repo content"). Deliberately NOT Fingerprint:
// Fingerprint additionally hashes the repo set, which is exactly the input
// this key must exclude — two fingerprints sharing the same
// (base, runtimeVersion, epoch) but naming different repos MUST resolve to
// the SAME cache volume, or the cache would recreate the very cold start
// it exists to remove (§19.1: "a cache that is not shared across repo sets
// recreates the very cold-start it exists to remove"). This is what makes
// the volume useful the very first time a SECOND Environment (a different
// repo set, same base image and runtime) is ever built, rather than only
// on a repeat build of the identical repo set Fingerprint itself already
// caches at the image level.
//
// # epoch: the rotation escape hatch Base/RuntimeVersion alone do not give
//
// Before epoch existed, the ONLY way to force a fresh cache volume for a
// (base, runtimeVersion) pair that had become unusable (corrupted beyond
// whatever a build service's own integrity check catches, or simply
// grown past a size bound with no eviction, §19.1's own named size-bound
// gap) was to bump RuntimeVersion — but RuntimeVersion is also a
// Fingerprint input (§19.1), so that same bump invalidates every shared
// IMAGE fleet-wide too (§19.1's own "simultaneous-invalidation cliff"),
// forcing every Environment's first post-bump build into the same window
// purely to escape a cache-volume problem that has nothing to do with the
// images themselves. epoch decouples the two: it participates in
// CacheVolumeKey but deliberately NOT in Fingerprint, so bumping it (an
// operator-controlled value, platform.Config.CacheVolumeEpoch, threaded
// through app/imagebuild.Builder — never a hard-coded literal) mints a
// brand-new, empty cache volume for every (base, runtimeVersion) pair at
// once, while every already-`ready` image stays exactly as valid and
// servable as before — the CACHE is purely an accelerator (ports.
// CacheMount's own "purely advisory" contract), so rotating it can never
// invalidate anything that matters for correctness, only reset how warm
// the next build's cache starts out.
//
// A plain SHA-256 hex digest over base, runtimeVersion, and epoch,
// NUL-separated (mirroring Fingerprint's own writeField/collision-
// avoidance reasoning: base="a"+runtimeVersion="bc" must never collide
// with base="ab"+runtimeVersion="c", now extended to a third field) — not
// cryptographically sensitive, a deterministic cache key exactly like
// Fingerprint itself, not a secret.
func CacheVolumeKey(base, runtimeVersion, epoch string) string {
	h := sha256.New()
	writeField(h, base)
	writeField(h, runtimeVersion)
	writeField(h, epoch)
	return hex.EncodeToString(h.Sum(nil))
}
