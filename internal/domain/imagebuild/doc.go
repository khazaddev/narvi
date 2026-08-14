// Package imagebuild implements the pure decision functions Step 26
// ("image builds") needs (§8.5-note, §10-P2, §3.5), redefined by Step 41
// ("warm boot: shared fingerprint + spawn-path simplification", §19.1):
//
//   - Fingerprint (fingerprint.go): a deterministic hash of
//     (base, repos, runtimeVersion), where repos maps repo name to its
//     NORMALIZED clone URL (NormalizeRepoURL), not a resolved SHA -- §19.1's
//     redefinition of §10 Phase 2's original "fingerprint = repo SHAs +
//     runtime version". This re-keys an image on scope/SHA-independent
//     inputs (one shared image per distinct repo set, continuously
//     refreshed from each repo's default-branch tip, §19.2) rather than one
//     image per exact SHA combination -- naming exactly one image-build
//     target, regardless of Go's own randomized map iteration order (map
//     keys are sorted before hashing) or of harmless URL spelling variance
//     (host case, a trailing slash, a trailing ".git" suffix).
//   - EvaluateBackoff (backoff.go): the exponential-backoff + failure-streak
//     decision for a failed build attempt (§3.5: "Failed image builds retry
//     with exponential backoff (not fixed 30 min) and alert on streaks"),
//     mirroring internal/domain/sandbox.EvaluateCircuitBreaker's own shape
//     (a Config struct populated from platform.Timeouts by the caller, a
//     Decision struct returned) and ImageBuildStreakThreshold mirroring
//     sandbox.CircuitBreakerThreshold's own "plain int, not a duration"
//     convention.
//   - CacheVolumeKey/WellKnownCachePaths/PruneCacheVersions (cachemount.go):
//     Step 43(c)'s own build-time dependency cache (§19.1's closing
//     paragraph), third iteration — immutable versioned cache snapshots.
//     CacheVolumeKey names a cache LINEAGE (base, runtimeVersion only,
//     never repo content, never a rotation epoch — versions make rotation
//     unnecessary, see that function's own doc comment); PruneCacheVersions
//     is the pure "keep the newest RetainedCacheVersions" retention
//     decision app/imagebuild.Builder applies to its own Postgres
//     bookkeeping after every confirmed publish.
//
// Every function here is pure per §11: no I/O, no time.Now(), no
// randomness -- "now" is always an explicit parameter, and backoff
// durations are always caller-supplied Config fields sourced from
// platform.Timeouts, never literals. This package does not import
// internal/platform (§1: domain has zero external dependencies).
package imagebuild
