package ports

import (
	"fmt"
	"reflect"

	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
)

// CreateSpec is what CreateSandbox and RestoreFromSnapshot need to bring
// up a sandbox instance (§4.1: "Spec includes gen + full SESSION_CONFIG
// doc").
//
// SessionConfig travels as ONE opaque document (§4.1: "sandbox env passed
// as one SESSION_CONFIG JSON document — the provider never assembles env
// fragments"): an implementation serializes the whole SessionConfig
// struct as a single JSON blob it hands to the sandbox/provider API,
// never spreading its fields across separate request parameters or env
// entries it would have to reassemble. Beyond that document, a provider
// only needs the fields below to physically create the sandbox instance.
//
// Gen is the one deliberate exception to "don't duplicate SessionConfig":
// it is intentionally also a top-level field (see its own doc comment),
// so nothing automatically keeps the two copies in sync. Every caller
// MUST call Validate before handing a CreateSpec to a SandboxProvider —
// CreateSandbox and RestoreFromSnapshot both do this themselves, so a
// diverging pair is rejected uniformly regardless of provider.
type CreateSpec struct {
	// Gen is the spawn generation being created (§3.2 fencing). Kept as
	// its own top-level field — rather than requiring callers to reach
	// into SessionConfig.Gen — so a provider (or the code driving it)
	// can gen-fence before it ever needs to touch, let alone parse, the
	// SESSION_CONFIG document itself. MUST equal SessionConfig.Gen; see
	// Validate.
	Gen int

	// SessionConfig is the full SESSION_CONFIG document (§6.4), passed
	// through opaquely: an implementation serializes it as one JSON
	// body: it does not read individual fields to reassemble its own
	// request shape.
	SessionConfig sessionconfig.SessionConfig

	// Image is the identifying reference of the image to boot the
	// sandbox from — a base image tag, or a previously built ImageRef
	// (see BuildImage) — the minimal thing a provider needs beyond
	// SESSION_CONFIG to know what filesystem/runtime to start. Empty
	// means "the provider's own default base image."
	Image string

	// Docker is the second deliberate exception to "don't duplicate
	// SessionConfig" (§27.5) — kept as its own top-level field,
	// like Gen, so a provider can act on this Environment's own
	// docker_required flag WITHOUT ever parsing the opaque SESSION_CONFIG
	// document itself (the Modal adapter's own CreateSandbox/
	// RestoreFromSnapshot map this directly onto Modal's VM runtime
	// option). MUST equal SessionConfig.Docker; see Validate.
	Docker bool

	// EgressPolicy is the third such exception (§27.6), carried
	// exactly like Docker above for the identical reason: the provider
	// substrate enforces this (Modal's own sandbox network controls), so
	// it must be reachable without parsing SESSION_CONFIG. Nil means "no
	// egress policy attached to this Environment" — today's unchanged,
	// unrestricted behavior, mirroring SessionConfig.EgressPolicy's own
	// nil zero value. MUST be structurally equal to
	// SessionConfig.EgressPolicy (nil-ness, Mode, and Allowlist contents
	// all included); see Validate.
	EgressPolicy *sessionconfig.SessionConfigEgressPolicy
}

// GenMismatchError is returned by CreateSpec.Validate when Gen and
// SessionConfig.Gen disagree. The two fields are a deliberate duplicate
// (see CreateSpec's doc comment) with nothing structurally keeping them
// in sync, so a caller-side bug that sets one and forgets the other must
// be caught before either copy reaches a provider — a silent divergence
// here is exactly the class of gen-fencing bug §3.2 exists to prevent.
type GenMismatchError struct {
	Gen              int
	SessionConfigGen int
}

func (e *GenMismatchError) Error() string {
	return fmt.Sprintf(
		"ports: CreateSpec.Gen (%d) does not match CreateSpec.SessionConfig.Gen (%d)",
		e.Gen, e.SessionConfigGen,
	)
}

// DockerMismatchError is returned by CreateSpec.Validate when Docker and
// SessionConfig.Docker disagree — the identical deliberate-duplicate
// safety net GenMismatchError provides for Gen, applied to CreateSpec's
// second such field (§27.5).
type DockerMismatchError struct {
	Docker              bool
	SessionConfigDocker bool
}

func (e *DockerMismatchError) Error() string {
	return fmt.Sprintf(
		"ports: CreateSpec.Docker (%t) does not match CreateSpec.SessionConfig.Docker (%t)",
		e.Docker, e.SessionConfigDocker,
	)
}

// EgressPolicyMismatchError is returned by CreateSpec.Validate when
// EgressPolicy and SessionConfig.EgressPolicy disagree — the identical
// safety net for CreateSpec's third deliberate-duplicate field (§27.6).
type EgressPolicyMismatchError struct {
	EgressPolicy              *sessionconfig.SessionConfigEgressPolicy
	SessionConfigEgressPolicy *sessionconfig.SessionConfigEgressPolicy
}

func (e *EgressPolicyMismatchError) Error() string {
	return fmt.Sprintf(
		"ports: CreateSpec.EgressPolicy (%+v) does not match CreateSpec.SessionConfig.EgressPolicy (%+v)",
		e.EgressPolicy, e.SessionConfigEgressPolicy,
	)
}

// Validate reports whether spec is internally consistent. Every
// SandboxProvider implementation must call this before using spec (Modal
// does so in CreateSandbox/RestoreFromSnapshot), so a diverging
// Gen/SessionConfig.Gen, Docker/SessionConfig.Docker, or
// EgressPolicy/SessionConfig.EgressPolicy pair is rejected uniformly
// regardless of which provider it was headed to.
func (s CreateSpec) Validate() error {
	if s.Gen != s.SessionConfig.Gen {
		return &GenMismatchError{Gen: s.Gen, SessionConfigGen: s.SessionConfig.Gen}
	}
	if s.Docker != s.SessionConfig.Docker {
		return &DockerMismatchError{Docker: s.Docker, SessionConfigDocker: s.SessionConfig.Docker}
	}
	if !reflect.DeepEqual(s.EgressPolicy, s.SessionConfig.EgressPolicy) {
		return &EgressPolicyMismatchError{EgressPolicy: s.EgressPolicy, SessionConfigEgressPolicy: s.SessionConfig.EgressPolicy}
	}
	return nil
}

// ImageSpec is what BuildImage needs to build (or have the provider reuse
// a cached build of) a sandbox image (§4.1: "image prebuilds are IN the
// interface"). §10 Phase 2 gave the original fingerprinting policy
// ("fingerprint = repo SHAs + runtime version; always fall back to base
// image on any miss — never block a session"); §19.1 ("warm boot: shared
// fingerprint") redefines the KEY (domain/imagebuild.Fingerprint
// now hashes each repo's normalized clone URL, not a resolved SHA — one
// shared image per repo set) while keeping BuildImage itself pinned to
// concrete SHAs: Repos below carries BOTH the URL (what the build service
// clones) and the concrete SHA it clones at (what makes the build
// reproducible) per repo — the fingerprint/cache KEY and the ACTUAL build
// inputs are deliberately different shapes now, where previously they
// were the same map.
type ImageSpec struct {
	// Base is the base image reference to build from (a registry
	// tag/digest).
	Base string

	// Repos carries, per repo name, the clone URL and the concrete SHA
	// to build from — everything the build service needs to do a real,
	// full (non-shallow) clone (§19.1: "build service bakes
	// /narvi/image-manifest.json and full clones"), keyed by repo name
	// so a multi-repo session's image build is reproducible from
	// exactly these inputs. NOT the same shape as the fingerprint's own
	// key input (domain/imagebuild.Fingerprint's repos map, name->URL
	// only, no SHA) — the fingerprint intentionally excludes the SHA so
	// a push to any repo doesn't mint a new image key; BuildImage still
	// needs a concrete SHA per repo to actually build something
	// reproducible.
	Repos map[string]RepoRef

	// RuntimeVersion is the pinned toolchain/runtime version baked into
	// the image (part of the same fingerprint).
	RuntimeVersion string

	// CacheMount, when non-nil, asks BuildImage to accelerate the build by
	// mounting a persistent, provider-backed cache volume into the build
	// sandbox at CacheMount.Paths (§19.1's own "build-time dependency
	// cache"). See CacheMount's own doc comment for the full
	// contract; nil means "no cache requested" (every ImageSpec literal
	// that predates this field, and every test fixture that never sets
	// it, keeps behaving exactly as before this field existed).
	CacheMount *CacheMount
}

// CacheMount is ImageSpec.CacheMount's own value type (§19.1's closing
// paragraph(c), third iteration): a request to mount one specific,
// already-published, IMMUTABLE cache version read-only into a build
// sandbox, and to publish a brand-new version if this build succeeds — to
// avoid re-downloading every dependency from a cold, empty filesystem on
// every single build.
//
// # Immutable versioned snapshots, not a shared mutable volume
//
// This design has been through three iterations; the first two are
// recorded here because the mistakes are the reason this shape looks the
// way it does, not merely history. Attempt 1 mounted one shared,
// persistent volume READ-WRITE fleet-wide and skipped a lock on the false
// premise that every well-known cache path is content-addressed (checked
// against each package manager's own real layout and found false for
// nearly every path — domain/imagebuild.WellKnownCachePaths' own doc
// comment has the full findings). Attempt 2 mounted that SAME shared
// volume READ-ONLY for the duration of a build, with exactly one
// unguarded write-back merged in after success — narrower, but still a
// writer into state a concurrent reader could observe, which is the
// identical hazard attempt 1 had, just smaller: the claim "nothing writes
// while a build can observe it" was false, because the write-back itself
// was exactly that.
//
// This (third) iteration removes the write window instead of narrowing it
// further. There is no shared mutable volume at all. Key names a cache
// LINEAGE; every successful build that mounts it publishes a brand-new,
// distinctly-numbered, immutable version under that lineage — an object
// that, once published, is never again written to by anyone. A build
// mounts exactly ONE specific version (MountVersion) read-only for its
// entire duration and cannot observe any version published after it
// started, because publishing creates a new object, it never touches one
// that already exists. "The version a build reads" and "the version a
// build might write" are therefore always two DIFFERENT identifiers by
// construction (MountVersion is some already-confirmed past publication;
// PublishVersion is freshly minted, strictly newer than every version
// this control plane has confirmed for this Key so far) — a build can
// never be handed a MountVersion equal to its own PublishVersion, so it
// can never even in principle observe its own not-yet-finished write.
//
// # Purely advisory — never a promise, never a requirement
//
// CacheMount is a HINT, not a contract obligation on the caller's part or a
// guarantee on the provider's: BuildImage's signature carries no
// cache-specific ERROR, and it must never grow one. Concretely: an adapter
// (or its backing build infrastructure) that cannot safely honor this
// request for ANY reason — no persistent-storage primitive at all, a
// corrupted volume, MountVersion no longer present (already reclaimed,
// e.g. after this control plane's own retention pruning — see
// domain/imagebuild.PruneCacheVersions), one it simply cannot reach right
// now, or a hang/unparseable response — MUST silently decline the mount
// and perform an ordinary cold build instead, exactly as if CacheMount had
// been nil. A caller (app/imagebuild.Builder) must never be able to
// distinguish "the cache was used" from "the cache was declined" via any
// ERROR path — both are success. What DOES change, deliberately, from
// attempt 2: BuildOutcome.PublishedCacheVersion (a plain SUCCESS-path
// return value, never an error) tells the caller whether THIS successful
// call's own request still carried the mount, because the immutable-
// version model needs that one fact to decide whether recording a new
// confirmed publication is honest — see BuildOutcome's own doc comment for
// why this is not a violation of "purely advisory." Every adapter whose
// Capabilities().ImageBuilds is false (RWX today, §4.1.1) never even
// reaches this field; RWX's own content-addressed layer cache already
// gives it this effect natively — corroboration that the model is
// workable, not merely appealing.
//
// # Key: Base + RuntimeVersion — never repo content, never a rotation epoch
//
// Key is domain/imagebuild.CacheVolumeKey(spec.Base, spec.RuntimeVersion) —
// deliberately excludes Repos, so every ImageSpec sharing the same Base
// and RuntimeVersion resolves to the SAME cache lineage regardless of
// which repo set it is building (one shared lineage across every
// Environment built from the same base image and runtime, not one per
// Fingerprint). A cache keyed on repo content would recreate the exact
// cold start this design exists to remove: the very first build of a
// brand-new Environment would get zero benefit from every OTHER
// Environment's already-warmed dependency downloads.
//
// Attempt 2 also took a rotation epoch (platform.Config.CacheVolumeEpoch)
// as a THIRD key input, folded into the volume's key but never into
// Fingerprint — the only way to abandon a mutable volume that had gone bad
// without also bumping RuntimeVersion (which, being a Fingerprint input
// too, invalidated every shared IMAGE fleet-wide). That escape hatch is
// gone in this iteration, deliberately, not merely unported: once every
// version is individually addressable and immutable, "escape a bad
// version" no longer needs a rotation primitive of its own — it falls out
// of the version history for free. A bad PUBLISHED version is escaped by
// simply not offering it as a future MountVersion: an operator deletes its
// row from this control plane's own confirmed-version bookkeeping
// (ImageCacheVersionStore.DeleteVersions — the same mechanism
// domain/imagebuild.PruneCacheVersions' own retention pruning already
// uses, see that function's own doc comment for why deleting a bookkeeping
// row is always safe), which makes LatestVersion naturally fall back to
// the next-most-recent — still-good — row. No config surface, no redeploy,
// no second rotation mechanism living alongside this one.
//
// # A lock is meaningless — not merely unnecessary
//
// §19.2's refresh pump makes concurrent BuildImage calls against the SAME
// cache Key routine, not rare. This port deliberately takes NO lock —
// neither here nor in any caller — around concurrent access to one cache
// lineage. Attempts 1 and 2 each argued their way to "no lock" from a
// premise that turned out to be false or incomplete (see this doc
// comment's own opening section). This iteration does not need an
// argument at all: there is no shared MUTABLE state left for a lock to
// guard. Two builds mounting the SAME MountVersion are both reading an
// object that has been finished and immutable since before either of them
// started; two builds publishing DIFFERENT PublishVersions under the same
// Key are each creating their own distinct object neither can observe
// until it exists in full. A tool's own host-local advisory lock (Go's
// `cache/lock`, Gradle's `modules-2.lock`, ...) stays irrelevant for the
// same reason it always was: it is invisible across sandboxes on different
// hosts regardless of what this design does.
//
// A package manager that needs to WRITE during a build (nearly all of them
// do, at least for a newly-resolved dependency) still needs somewhere to
// write: it gets a private, per-build writable layer at these same
// logical paths, seeded read-through from the mounted MountVersion — an
// ordinary copy-on-write overlay shape whose exact mechanism (a real
// overlay filesystem, a seeded scratch copy, ...) is the adapter's or
// build service's own concern, external to this repository, exactly as
// the mount itself and the dependency install already are (this
// codebase's own Go code performs no such write itself). On success, that
// private layer's own accumulated changes are what gets captured and
// published as PublishVersion — a genuinely NEW, distinct object, never a
// mutation of MountVersion's own bytes, and never visible to any OTHER
// build until this one has both succeeded AND had that publication
// confirmed (BuildOutcome.PublishedCacheVersion, recorded by
// app/imagebuild.Builder via ImageCacheVersionStore.PublishVersion). See
// domain/imagebuild.WellKnownCachePaths' own doc comment for the per-tool
// read-only-cache posture this design could actually verify.
//
// # Documented obligation on the adapter/build service
//
// Everything above the wire is this repository's own, enforceable
// contract (this struct's shape, BuildOutcome's own confirmation field,
// and the version-history bookkeeping in
// internal/adapters/outbound/postgres.ImageCacheVersionStore). What
// happens BELOW the wire — how a build service actually stores and
// addresses an immutable version, how it seeds a build's own writable
// overlay from MountVersion's content, how it captures that overlay's
// changes into a new object at PublishVersion — is genuinely external to
// this repository (the storage mechanics belong to the build service, the
// same boundary §19.1 already draws around the mount itself and the
// dependency install). This port states the obligation explicitly rather
// than describing it as implemented: a conformant adapter/build service
// MUST (a) never modify the bytes addressed by an already-published
// version, once published, for any reason; (b) make a version reachable
// via MountVersion by any OTHER build's request only after — and only if —
// THIS build's own request reported success back through BuildImage; and
// (c) treat an unrecognized, missing, or otherwise-unusable MountVersion
// as ordinary cache-mount trouble (decline and proceed cold), never as a
// request failure. Nothing in this repository can verify a real build
// service actually honors (a)-(c) — the same honestly-named limitation
// §19.1 already accepts for the mount and the write-through/write-back
// mechanism generally.
//
// # Cross-tenant provenance is unchanged by any of this — stated plainly
//
// One cache Key is still shared fleet-wide across every repo set built
// from the same Base/RuntimeVersion — versioning changes NOTHING about
// that. A published version's bytes were still produced by running some
// OTHER repo's own setup.sh (§19.1's own "its real payload is the warm
// dependency caches"), and every subsequent build that mounts that
// version as its own MountVersion still executes whatever it contains.
// Immutability guarantees WHEN a version is safe to read (never
// concurrently with its own creation) — it says nothing, and was never
// intended to say anything, about WHOSE code produced what a build reads.
// This is exactly as true of this iteration as it was of attempts 1 and 2;
// it is recorded here explicitly so it is never mistaken for something
// this correction fixed.
//
// # Retention: bounded, not unbounded — see domain/imagebuild
//
// Immutable versions accumulate (every successful, cache-requesting build
// mints one) — left unchecked this would make the original "cache volume
// size is unbounded" gap WORSE, not better. domain/imagebuild.
// RetainedCacheVersions and PruneCacheVersions specify the concrete policy
// (keep the newest N per Key; app/imagebuild.Builder prunes this control
// plane's own bookkeeping immediately after every confirmed publish; see
// PruneCacheVersions' own doc comment for exactly what a reader does if
// its own already-resolved MountVersion is later pruned — nothing
// special, by construction). Reclaiming the underlying provider-side
// bytes for a pruned version remains the documented, unimplemented
// build-service obligation named in the section above — real enforcement
// there (a byte cap, provider-side GC keyed off this control plane's own
// retained set, or both) is deliberately deferred to a later Step, exactly
// mirroring §19.2's own "newly urgent, still deferred" image-GC posture.
//
// # Per-provider semantics differ — the port must not assume one
//
// HOW an adapter turns (Key, MountVersion, PublishVersion, Paths) into a
// real read-only mount of one immutable object plus a distinct newly
// published one — and whether it can safely do so at all for a given
// request — is entirely that adapter's own decision. See
// internal/adapters/outbound/modal's own BuildImage for the one
// implementation that exists today: it declines the mount and
// transparently retries as a cold build the instant its own wire protocol
// reports cache trouble (a fixed, closed set of structured codes covering
// corruption, unavailability, a build-service-reported internal timeout,
// and a not-found/pruned version — see modal/errors.go's
// isCacheMountTrouble for the full, current set and for why a raw
// CLIENT-side transport timeout is deliberately NOT one of these signals:
// a bare timeout already consumed this request's entire
// ProviderHTTPClientTimeout budget, so treating it as cache trouble and
// retrying cold can only cost MORE time, never less, whenever the
// underlying delay was not actually caused by the cache — the exact
// "doubling wall clock, guaranteeing failure" defect this iteration
// removes rather than narrows).
type CacheMount struct {
	// Key names the cache LINEAGE this build reads a version from and
	// publishes a new version into —
	// domain/imagebuild.CacheVolumeKey(Base, RuntimeVersion). Opaque
	// outside whichever adapter mounts it; this package makes no claim
	// about its own format beyond "deterministic for a given
	// (Base, RuntimeVersion) pair."
	Key string

	// MountVersion is the specific, already-published, IMMUTABLE version
	// under Key this build should mount READ-ONLY as its dependency-cache
	// source — the most recent version app/imagebuild.Builder has already
	// confirmed was published (ImageCacheVersionStore.LatestVersion).
	// Empty means no version has ever been confirmed under this Key yet
	// (this Key's very first cache-requesting build, or every prior
	// attempt failed before confirming a publish) — an ordinary cold
	// build with nothing to mount, which still requests PublishVersion
	// below so a LATER build gets something to mount from. Always
	// strictly different from PublishVersion on the SAME CacheMount (see
	// this struct's own top doc comment) — a build can never be handed a
	// MountVersion identical to what it is itself about to publish.
	MountVersion string

	// PublishVersion names the NEW, immutable version this build's own
	// outputs will be published under — if, and only if, this build
	// succeeds AND the adapter's own eventual successful request still
	// carried this CacheMount (BuildOutcome.PublishedCacheVersion echoes
	// this value back exactly when that held; empty otherwise). A build
	// that fails, or whose mount was declined by the adapter's own
	// decline-and-retry-cold fallback, publishes nothing: this value is
	// simply abandoned, never reused, never retried under the same
	// number — a harmless gap, exactly like a database sequence's own
	// gap-on-rollback behavior. Minted by app/imagebuild.Builder via
	// ImageCacheVersionStore.MintVersion before every real BuildImage
	// attempt that requests a cache mount at all.
	PublishVersion string

	// Paths is the package-manager-agnostic, fixed, closed set of
	// well-known cache directories to mount, inside the build sandbox, at
	// the version named by MountVersion (read-only) and to capture into
	// the version named by PublishVersion on success —
	// domain/imagebuild.WellKnownCachePaths(), see that function's own doc
	// comment for the full list and why it stays fixed rather than
	// guessing which ecosystem(s) a given repo set actually uses. A fresh
	// slice on every call (never a shared backing array a caller could
	// accidentally mutate across builds).
	Paths []string
}

// RepoRef names one repo's clone URL and the concrete SHA to build the
// image from — ImageSpec.Repos' own value type (§19.1).
type RepoRef struct {
	// URL is the repo's clone URL (sessionconfig.SessionConfig.Repos[].Url,
	// or the fingerprint-input's own normalized form — either way, what
	// the build service actually clones from).
	URL string

	// SHA is the concrete commit the build service clones/checks out to
	// — always a real SHA by the time BuildImage is called, never a
	// branch name (§19.1: "The builder resolves each default-branch tip
	// SHA at claim time and passes concrete SHAs to BuildImage — builds
	// stay pinned and reproducible; only the key is SHA-free").
	SHA string
}
