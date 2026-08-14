package ports

import "context"

// SandboxProvider is the complete port a sandbox compute provider
// implements (§4.1: "complete — no out-of-interface operations" — every
// operation a provider needs to support is listed here; nothing about a
// provider is reached through any other channel).
//
// Modal (internal/adapters/outbound/modal) implements this interface
// starting this Step; RWX (internal/adapters/outbound/rwx) implements the
// SAME interface starting Step 57. Nothing provider-specific may leak
// into this package or this signature — see doc.go and CLAUDE.md's "don't
// couple a port to a single adapter."
type SandboxProvider interface {
	// Capabilities reports which of the optional operations below this
	// provider actually supports, so callers branch on a real capability
	// flag instead of assuming every provider behaves identically.
	Capabilities() Capabilities

	// CreateSandbox creates a brand-new sandbox instance from spec.
	// §4.1: "Spec includes gen + full SESSION_CONFIG doc" — spec.Gen is
	// the spawn generation (§3.2 fencing) and spec.SessionConfig is the
	// single SESSION_CONFIG document the provider passes through
	// opaquely (see CreateSpec).
	CreateSandbox(ctx context.Context, spec CreateSpec) (SandboxRef, error)

	// StopSandbox explicitly stops a running sandbox instance. Optional
	// per Capabilities().ExplicitStop.
	StopSandbox(ctx context.Context, ref SandboxRef) error

	// ResumeSandbox resumes the SAME underlying provider sandbox
	// instance in place (§3.2: "stopped|stale + resume-capable ->
	// resume (same provider sandbox)"), as opposed to
	// RestoreFromSnapshot, which always creates a new instance/gen.
	//
	// Optional per Capabilities().Resume. A provider that reports
	// Resume: false MUST return a permanent ProviderError here instead
	// of silently no-oping or panicking, so a caller that calls it
	// without consulting Capabilities first still gets a sane, typed
	// failure.
	ResumeSandbox(ctx context.Context, ref SandboxRef) error

	// TakeSnapshot snapshots a live sandbox instance for a later
	// RestoreFromSnapshot. Optional per Capabilities().Snapshots.
	TakeSnapshot(ctx context.Context, ref SandboxRef) (SnapshotID, error)

	// RestoreFromSnapshot creates a NEW sandbox instance — a new gen,
	// per §3.2: "stopped|stale + snapshot -> restore (new gen)" — seeded
	// from a previously taken snapshot. Optional per
	// Capabilities().Snapshots.
	RestoreFromSnapshot(ctx context.Context, id SnapshotID, spec CreateSpec) (SandboxRef, error)

	// BuildImage starts (or has the provider reuse a cached) image
	// build (§4.1: "image prebuilds are IN the interface"). Optional per
	// Capabilities().ImageBuilds. The full image-build workflow —
	// fingerprinting policy, rebuild scheduling with backoff,
	// fallback-to-base-on-any-miss (§10 Phase 2) — is a later Step; this
	// Step only requires the method to exist with a real implementation
	// behind it.
	//
	// spec.CacheMount (§19.1's own "build-time dependency cache", Step
	// 43(c); third iteration — immutable versioned cache snapshots)
	// optionally requests a specific, immutable, already-published cache
	// version be mounted read-only for this build, plus a NEW version to
	// publish if this build succeeds — see CacheMount's own doc comment
	// for the full contract (advisory only; a corrupted, unavailable,
	// hung, not-found/pruned, or simply unsupported cache MUST degrade to
	// an ordinary cold build, never a BuildImage failure).
	// BuildOutcome.PublishedCacheVersion reports back whether THIS
	// successful call's own request still carried that CacheMount, so a
	// caller can tell whether to record a real publication or nothing at
	// all — see BuildOutcome's own doc comment.
	BuildImage(ctx context.Context, spec ImageSpec) (BuildOutcome, error)

	// DeleteImage deletes a previously built image. Optional per
	// Capabilities().ImageBuilds.
	DeleteImage(ctx context.Context, ref ImageRef) error

	// List returns every sandbox instance this provider currently knows
	// about, for reconciliation/orphan GC (§4.1: "for
	// reconciliation/GC").
	List(ctx context.Context) ([]SandboxRef, error)
}
