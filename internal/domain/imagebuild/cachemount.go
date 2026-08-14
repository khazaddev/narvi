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
//   - npm's own content-addressed cache (the design's own worked example:
//     "the filename IS the content hash").
//   - Yarn Classic/Berry's global cache.
//   - pnpm's own content-addressed store.
//   - pip's HTTP cache (the design's own second worked example).
//   - Go's module cache, GOMODCACHE's own default location (the design's
//     own third worked example).
//   - Go's build cache, GOCACHE's own default location — distinct from the
//     module cache above, but the identical content-addressed-by-input-hash
//     property applies (§19.1's own no-lock rationale, ports.CacheMount).
//   - Cargo's downloaded-crate registry cache.
//   - Composer's (PHP) package cache.
//   - Bundler's (Ruby) gem cache.
//   - Maven's local repository cache.
//   - Gradle's dependency cache.
var WellKnownCachePaths = []string{
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

// CacheVolumeKey deterministically names the ONE persistent cache volume a
// build against (base, runtimeVersion) should mount (§19.1's own closing
// paragraph: "keyed on Base + RuntimeVersion ONLY — never on repo
// content"). Deliberately NOT Fingerprint: Fingerprint additionally hashes
// the repo set, which is exactly the input this key must exclude — two
// fingerprints sharing the same (base, runtimeVersion) but naming different
// repos MUST resolve to the SAME cache volume, or the cache would recreate
// the very cold start it exists to remove (§19.1: "a cache that is not
// shared across repo sets recreates the very cold-start it exists to
// remove"). This is what makes the volume useful the very first time a
// SECOND Environment (a different repo set, same base image and runtime)
// is ever built, rather than only on a repeat build of the identical repo
// set Fingerprint itself already caches at the image level.
//
// A plain SHA-256 hex digest over base and runtimeVersion, NUL-separated
// (mirroring Fingerprint's own writeField/collision-avoidance reasoning:
// base="a"+runtimeVersion="bc" must never collide with base="ab"+
// runtimeVersion="c") — not cryptographically sensitive, a content-
// addressed cache key exactly like Fingerprint itself, not a secret.
func CacheVolumeKey(base, runtimeVersion string) string {
	h := sha256.New()
	writeField(h, base)
	writeField(h, runtimeVersion)
	return hex.EncodeToString(h.Sum(nil))
}
