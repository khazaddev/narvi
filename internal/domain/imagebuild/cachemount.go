package imagebuild

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
)

// WellKnownCachePaths is the fixed, closed, package-manager-agnostic set of
// cache directories §19.1's own "build-time dependency cache"
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
// # Mounted read-only, from one specific IMMUTABLE version — never a claim
// # about content-addressing, and never a lock
//
// This design has now been through three iterations, and the first two are
// recorded here because the mistakes are instructive, not merely historical.
//
// Attempt 1 mounted these paths READ-WRITE and justified skipping a lock on
// the grounds that every path here is a package manager's own
// "content-addressed" cache, so concurrent writers could only ever produce
// identical bytes at the identical name. That premise was checked against
// each package manager's own real, documented on-disk layout and found
// false for nearly every path below: Go's module cache ships its OWN lock
// file (`cache/lock`) and mutable `@v/list` version listings precisely
// because concurrent access is NOT safe without coordination; Gradle's
// `caches/modules-2` is binary metadata mutated in place, guarded by its
// own `modules-2.lock`/`journal-1.lock`; Cargo guards its registry with
// `.package-cache` (not even mounted here); npm's own `_cacache/index-v5`
// entries are keyed by the hash of the REQUEST URL, not the response
// content, and are written by APPEND, not atomic rename; pip's cache key is
// the SHA-224 of the request URL, and its `wheels/` subtree holds locally
// built artifacts; Composer, Bundler, and Yarn are similar.
//
// Attempt 2 mounted every path READ-ONLY for the duration of a build, with
// exactly one write-back merged into the SAME shared volume after that
// build succeeded — no lock, because "nothing writes while a build can
// observe it." That was still wrong: the write-back was ITSELF an
// unguarded writer into the shared volume, at exactly the moment some
// OTHER build could be reading from it. Narrowing the write window from
// "the whole build" to "one write-back" is not the same as removing it —
// a lock-free write into state a concurrent reader can observe is the
// identical hazard attempt 1 had, just smaller.
//
// This (third) attempt removes the write window rather than narrowing it
// further. Nothing is ever mutated in place, by anyone, ever. Every
// successful build publishes a brand-new, immutable, distinctly-named
// version; a build mounts exactly one specific, already-published version,
// read-only, for its entire duration, and there is no operation anywhere
// in this design that writes into a version once it has been published.
// Two consequences follow directly:
//
//   - A lock becomes MEANINGLESS, not merely unnecessary — there is no
//     shared mutable state left for a lock to guard. Two builds mounting
//     the SAME version are both reading the identical, already-finished,
//     never-again-written object; two builds publishing DIFFERENT versions
//     under the same key are each creating their own distinct object that
//     no one else's mount can observe until it exists in full. The
//     "content-addressed, so concurrent writers can only collide
//     harmlessly" argument attempt 1 got wrong for these specific tools no
//     longer needs to be made at all, for any tool, because there is no
//     concurrent writing into shared state to reason about in the first
//     place.
//   - Rotation (attempt 2's own `NARVI_CACHE_VOLUME_EPOCH` escape hatch,
//     minted for exactly one reason: force a fresh cache volume when the
//     current one had gone bad) falls out for free instead of needing its
//     own mechanism. A bad published version is escaped by pointing a new
//     build's own MountVersion at an earlier, known-good one — ordinary
//     history, not a rotation primitive. See ports.CacheMount's own doc
//     comment for exactly how that happens in this codebase today
//     (operator-driven, via the same version-history table retention
//     already prunes from) and why no config-level epoch is needed
//     anymore.
//
// A package manager that needs to WRITE during a build (nearly all of them
// do, at least for a newly-resolved dependency) still needs somewhere to
// write: the build gets a private, per-build writable layer at these same
// logical paths, seeded read-through from the mounted MountVersion — an
// ordinary copy-on-write overlay shape whose exact mechanism is the
// adapter's or build service's own concern, external to this repository,
// exactly as the mount itself and the dependency install already are. On
// success, that private layer's own accumulated changes are what gets
// published as the new PublishVersion — a DISTINCT object, never a mutation
// of MountVersion's own bytes. This is what makes "read-only" compatible
// with a tool like Go's module cache that is not documented to tolerate a
// literal, unassisted read-only cache directory on its own — see
// ports.CacheMount's own doc comment for the per-tool read-only-cache
// posture this design could actually verify, and the one tool (Go's own
// GOMODCACHE) it could not confirm degrades gracefully without that
// writable layer.
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
// (an audit-remediation finding: "WellKnownCachePaths is an
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

// CacheVolumeKey deterministically names the ONE cache lineage a build
// against (base, runtimeVersion) publishes immutable versions into and
// mounts them from (§19.1's own closing paragraph: "keyed on Base +
// RuntimeVersion... never on repo content"). Deliberately NOT Fingerprint:
// Fingerprint additionally hashes the repo set, which is exactly the input
// this key must exclude — two fingerprints sharing the same
// (base, runtimeVersion) but naming different repos MUST resolve to the
// SAME cache key, or the cache would recreate the very cold start it
// exists to remove (§19.1: "a cache that is not shared across repo sets
// recreates the very cold-start it exists to remove"). This is what makes
// the key useful the very first time a SECOND Environment (a different
// repo set, same base image and runtime) is ever built, rather than only
// on a repeat build of the identical repo set Fingerprint itself already
// caches at the image level.
//
// # No rotation-epoch parameter — versions make one unnecessary
//
// Earlier iterations of this design (attempt 2) took a third argument,
// epoch (platform.Config.CacheVolumeEpoch, an operator-controlled value):
// the ONLY way to force a fresh cache when the current shared, MUTABLE
// volume for a (base, runtimeVersion) pair had gone bad (corrupted, or
// grown past a size bound with no eviction) without also bumping
// RuntimeVersion — which is ALSO a Fingerprint input, so that same bump
// invalidated every shared IMAGE fleet-wide too (§19.1's own
// "simultaneous-invalidation cliff"), forcing every Environment's first
// post-bump build into the same window purely to escape a cache problem
// that had nothing to do with the images themselves.
//
// Once every version under a key is immutable and individually addressable
// (ports.CacheMount.MountVersion/PublishVersion), that escape hatch is no
// longer needed: a bad version is escaped by pointing a new build's own
// MountVersion at an earlier, known-good one — ordinary history, not a
// rotation primitive requiring its own config surface. This function
// therefore keeps its original two-argument shape rather than growing (or
// keeping) a third — see ports.CacheMount's own doc comment for exactly
// how "point at an earlier good version" is realized operationally in this
// codebase today.
//
// A plain SHA-256 hex digest over base and runtimeVersion, NUL-separated
// (mirroring Fingerprint's own writeField/collision-avoidance reasoning:
// base="a"+runtimeVersion="bc" must never collide with base="ab"+
// runtimeVersion="c") — not cryptographically sensitive, a deterministic
// cache key exactly like Fingerprint itself, not a secret.
func CacheVolumeKey(base, runtimeVersion string) string {
	h := sha256.New()
	writeField(h, base)
	writeField(h, runtimeVersion)
	return hex.EncodeToString(h.Sum(nil))
}

// RetainedCacheVersions is the fixed number of most-recently-published
// immutable cache versions app/imagebuild.Builder keeps tracked, per cache
// key, in this control plane's own bookkeeping (image_cache_versions) —
// §19.1's own named retention policy: "Immutable versions accumulate, which
// makes the unbounded-size problem worse, not better. Specify the
// retention policy."
//
// 5, not 1: keeping only the single newest version would make EVERY
// concurrently in-flight build (§19.2's refresh pump routinely runs many
// builds sharing one cache key at once, across different fingerprints on
// the same Base/RuntimeVersion) race retention itself — a build that
// resolved MountVersion two publishes ago, and is still running when a
// third and fourth publish land, would find its own already-sent
// MountVersion pruned from this control plane's bookkeeping before it
// even finishes (harmless per PruneCacheVersions' own doc comment, but
// needlessly wasteful — one more avoidable cold-fallback round trip). 5
// versions of headroom absorbs that ordinary in-flight concurrency without
// requiring this codebase to track which versions are still actively
// mounted (a reference-counting mechanism this design deliberately does
// NOT build — see PruneCacheVersions' own doc comment for why pruning is
// safe without one regardless), while still bounding the unbounded-growth
// gap this constant exists to close, rather than leaving it fully open.
// Not configurable: unlike the rotation epoch this design retires (see
// CacheVolumeKey's own doc comment), retention is a fixed policy, not an
// operator escape hatch — an operator who needs to force a specific
// earlier version back into service does so by pointing a build's own
// resolution at it directly (ports.CacheMount's own doc comment), not by
// tuning how many versions are kept.
const RetainedCacheVersions = 5

// PruneCacheVersions decides which of versions (every version currently
// tracked for ONE cache key, in any order, exactly as
// ImageCacheVersionStore.ListVersions returns them) this control plane's
// own bookkeeping should stop tracking: everything beyond the newest
// RetainedCacheVersions. Returns the versions to PRUNE (app/imagebuild.
// Builder's own ImageCacheVersionStore.DeleteVersions input), never the
// versions to keep — the caller has no separate use for the kept set.
//
// Pure and total per §11: no I/O, no time.Now(), no randomness — a slice
// copy, a sort, and a slice. versions is never mutated (PruneCacheVersions
// sorts its OWN copy) so a caller passing a slice it still holds a
// reference to elsewhere is never surprised by an in-place reorder.
//
// len(versions) <= RetainedCacheVersions returns an empty (nil) slice —
// there is nothing yet to prune, the ordinary case for a cache key that
// has not accumulated many versions.
//
// # What a reader does if its own pinned version was pruned
//
// Nothing special: PruneCacheVersions only ever removes THIS control
// plane's OWN bookkeeping row for a version (ImageCacheVersionStore.
// DeleteVersions' own doc comment) — it is never a request to reclaim the
// version's underlying bytes at the provider, and it never touches
// anything an already-in-flight build has already resolved and sent as
// its own MountVersion. A build that DOES eventually try to mount a
// version the provider has separately, independently reclaimed degrades
// exactly like any other cache-mount trouble — the adapter's own
// decline-and-retry-cold fallback (internal/adapters/outbound/modal),
// which recognizes a "version not found" structured code alongside its
// other cache-trouble signals — never a build failure, per the port's own
// pure-accelerator rule (ports.CacheMount's own doc comment).
func PruneCacheVersions(versions []int64) []int64 {
	if len(versions) <= RetainedCacheVersions {
		return nil
	}

	sorted := make([]int64, len(versions))
	copy(sorted, versions)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] > sorted[j] })

	prune := make([]int64, len(sorted)-RetainedCacheVersions)
	copy(prune, sorted[RetainedCacheVersions:])
	return prune
}
