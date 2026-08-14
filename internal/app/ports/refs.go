package ports

// SandboxRef is a provider's own opaque handle to one sandbox instance it
// created (via CreateSandbox or RestoreFromSnapshot) — the value every
// other per-instance method (StopSandbox, ResumeSandbox, TakeSnapshot,
// ...) takes back to identify which provider-side instance to act on.
//
// Different providers populate this differently: Modal's ProviderID
// (internal/adapters/outbound/modal) holds Modal's own sandbox object id;
// RWX (internal/adapters/outbound/rwx, Step 57), the second provider
// implementing this same interface, populates ProviderID with the
// deterministic per-(session, gen) identity string its own adapter
// derives (RWX itself keys a sandbox on branch + config-file path, not a
// separately-assigned opaque id — see that package's own doc comment).
// ports declares no assumption about that string's internal format or
// structure, and no code outside a specific provider adapter may parse
// it.
type SandboxRef struct {
	// ProviderID is the provider's own identifier for this sandbox
	// instance.
	ProviderID string
}

// SnapshotID is a provider's opaque identifier for one snapshot taken via
// TakeSnapshot, later passed back to RestoreFromSnapshot (§3.2:
// "stopped|stale + snapshot -> restore (new gen)"). Treated as an opaque
// string outside the provider that minted it.
type SnapshotID string

// BuildRef is a provider's opaque identifier for one BuildImage
// invocation (§4.1: "image prebuilds are IN the interface"; the full
// build-scheduling workflow — fingerprinting, backoff, fallback-to-base —
// is a later Step, §10 Phase 2). Treated as an opaque string outside the
// provider that minted it.
type BuildRef string

// BuildOutcome is BuildImage's own success return value (§19.1's closing
// paragraph, Step 43(c), third iteration: immutable versioned cache
// snapshots).
type BuildOutcome struct {
	// Ref is the provider's own opaque identifier for the built image —
	// exactly what BuildRef always was before this struct existed.
	Ref BuildRef

	// PublishedCacheVersion is spec.CacheMount.PublishVersion, ECHOED BACK
	// only when the adapter's own EVENTUAL, successful wire request still
	// carried that CacheMount — empty when spec.CacheMount was nil to
	// begin with, or when a requested mount was declined and the
	// adapter's own decline-and-retry-cold fallback (CacheMount's own doc
	// comment) dropped it before the attempt that ultimately succeeded.
	//
	// This is NOT a cache-specific error signal — BuildImage's signature
	// still carries none, and a caller must still never be able to
	// distinguish "declined" from "used" via any ERROR path (CacheMount's
	// own "purely advisory" contract is unchanged). It is the one fact
	// the immutable-version model genuinely needs on the SUCCESS path
	// alone: whether app/imagebuild.Builder may honestly record
	// spec.CacheMount.PublishVersion as this cache key's new latest
	// CONFIRMED version. Recording a version nothing actually published
	// would point every future build's own MountVersion at an object
	// that was never created — harmless (the adapter's own
	// decline-and-retry-cold fallback tolerates a missing/not-found
	// mount too), but a needless, avoidable acceleration loss this one
	// field prevents entirely, at zero cost to any caller that never
	// requested a CacheMount in the first place (empty either way).
	PublishedCacheVersion string
}

// ImageRef is a provider's opaque identifier for one previously built
// image, accepted by DeleteImage and (via CreateSpec.Image) referenced
// when creating a sandbox from a prebuilt image rather than a base image.
// Treated as an opaque string outside the provider that minted it.
type ImageRef string
