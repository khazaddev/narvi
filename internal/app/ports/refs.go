package ports

// SandboxRef is a provider's own opaque handle to one sandbox instance it
// created (via CreateSandbox or RestoreFromSnapshot) — the value every
// other per-instance method (StopSandbox, ResumeSandbox, TakeSnapshot,
// ...) takes back to identify which provider-side instance to act on.
//
// Different providers populate this differently: Modal's ProviderID
// (internal/adapters/outbound/modal) holds Modal's own sandbox object id;
// a future provider (RWX, Step 48) implementing this same interface would
// populate ProviderID with whatever ITS API calls an instance. ports
// declares no assumption about that string's internal format or
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

// ImageRef is a provider's opaque identifier for one previously built
// image, accepted by DeleteImage and (via CreateSpec.Image) referenced
// when creating a sandbox from a prebuilt image rather than a base image.
// Treated as an opaque string outside the provider that minted it.
type ImageRef string
