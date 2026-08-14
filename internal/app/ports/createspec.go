package ports

import (
	"fmt"

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

// Validate reports whether spec is internally consistent. Every
// SandboxProvider implementation must call this before using spec (Modal
// does so in CreateSandbox/RestoreFromSnapshot), so a diverging
// Gen/SessionConfig.Gen pair is rejected uniformly regardless of which
// provider it was headed to.
func (s CreateSpec) Validate() error {
	if s.Gen != s.SessionConfig.Gen {
		return &GenMismatchError{Gen: s.Gen, SessionConfigGen: s.SessionConfig.Gen}
	}
	return nil
}

// ImageSpec is what BuildImage needs to build (or have the provider reuse
// a cached build of) a sandbox image (§4.1: "image prebuilds are IN the
// interface"). §10 Phase 2 gave the original fingerprinting policy
// ("fingerprint = repo SHAs + runtime version; always fall back to base
// image on any miss — never block a session"); §19.1 ("warm boot: shared
// fingerprint", Step 41) redefines the KEY (domain/imagebuild.Fingerprint
// now hashes each repo's normalized clone URL, not a resolved SHA — one
// shared image per repo set) while keeping BuildImage itself pinned to
// concrete SHAs: Repos below carries BOTH the URL (what the build service
// clones) and the concrete SHA it clones at (what makes the build
// reproducible) per repo — the fingerprint/cache KEY and the ACTUAL build
// inputs are deliberately different shapes now, where before Step 41 they
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
	// cache" — Step 43(c)). See CacheMount's own doc comment for the full
	// contract; nil means "no cache requested" (every ImageSpec literal
	// that predates this field, and every test fixture that never sets
	// it, keeps behaving exactly as before this field existed).
	CacheMount *CacheMount
}

// CacheMount is ImageSpec.CacheMount's own value type (§19.1's closing
// paragraph, Step 43(c)): a persistent, provider-backed cache volume a
// BuildImage call may mount into its build sandbox, to avoid re-downloading
// every dependency from a cold, empty filesystem on every single build.
//
// # Purely advisory — never a promise, never a requirement
//
// CacheMount is a HINT, not a contract obligation on the caller's part or a
// guarantee on the provider's: BuildImage's signature carries no
// cache-specific error, and it must never grow one. §19.1's own words are
// exact: "a corrupted, locked, or unavailable cache degrades to exactly
// today's cold build and never fails one — this is the load-bearing
// property: treat every cache error as 'proceed without cache', never as a
// build error." Concretely: an adapter (or its backing build
// infrastructure) that cannot safely mount this volume for ANY reason —
// no persistent-volume primitive at all, a corrupted or stuck-locked
// volume, one it simply cannot reach right now — MUST silently decline the
// mount and perform an ordinary cold build instead, exactly as if
// CacheMount had been nil. A caller (app/imagebuild.Builder) must never be
// able to distinguish "the cache was used" from "the cache was declined"
// from BuildImage's return value alone — both are success. Every adapter
// whose Capabilities().ImageBuilds is false (RWX today, §4.1.1) never even
// reaches this field; RWX's own content-addressed layer cache already
// gives it this effect natively.
//
// # Key: Base + RuntimeVersion ONLY, never repo content
//
// Key is domain/imagebuild.CacheVolumeKey(spec.Base, spec.RuntimeVersion) —
// deliberately excludes Repos, so every ImageSpec sharing the same Base and
// RuntimeVersion resolves to the SAME cache volume regardless of which
// repo set it is building (one shared cache across every Environment built
// from the same base image and runtime, not one per Fingerprint). A cache
// keyed on repo content would recreate the exact cold start this design
// exists to remove: the very first build of a brand-new Environment would
// get zero benefit from every OTHER Environment's already-warmed
// dependency downloads.
//
// # No lock — the port's own documented concurrency contract
//
// §19.2's refresh pump makes concurrent BuildImage calls against the SAME
// cache Key routine, not rare: it rebuilds every ready shared image
// whenever its repos' tips move, and nothing serializes two different
// fingerprints that happen to share a Base/RuntimeVersion pair. This port
// deliberately takes NO lock — neither here nor in any caller — around
// concurrent access to one cache volume. That is a considered decision,
// not an oversight: every path in WellKnownCachePaths
// (internal/domain/imagebuild) names a package manager's own
// CONTENT-ADDRESSED cache (npm's _cacache, pip's HTTP cache, Go's module
// and build caches, ...) — the filename under each of those paths IS the
// content hash of what it holds, so two concurrent builds that both happen
// to fetch the identical package version write the IDENTICAL bytes to the
// IDENTICAL path. The "conflict" a lock would prevent is idempotent by
// construction: at worst, two writers redundantly produce the same file: a
// wasted download, never a corrupted cache. A lock would instead serialize
// §19.2's refresh pump — this design's own steady state, not an edge case
// — purely to guard against a collision that cannot corrupt anything.
//
// Two obligations follow from relying on that property instead of a lock,
// and both bind whoever actually writes into one of these paths (in
// practice the package-manager processes running inside the remote build
// sandbox, e.g. npm's own _cacache implementation — this codebase's own Go
// code performs no such write itself, since the mounting and the
// dependency install both happen inside the provider's build
// infrastructure, outside this repo's reach):
//
//  1. Every writer MUST rely on atomic rename semantics: write to a
//     temporary path in the SAME directory as the file's own final
//     content-addressed name, then rename it into place, rather than
//     writing in place under the final name. A concurrent reader must
//     never be able to observe a partially-written file under its final
//     name — exactly what makes the "two writers, one path" case above
//     safe rather than a race. Every ecosystem this codebase's own
//     WellKnownCachePaths names already implements this internally.
//  2. Writes must be committed to the persistent Key location only once
//     the build that produced them has itself succeeded — a failed build
//     must never leave partial or corrupt dependency-download artifacts
//     poisoning the shared cache for whichever build runs against Key
//     next. This is a contract on the provider/build-service side (the
//     same "documented, not implemented in this repo" posture §19.1
//     already takes for the full non-shallow clone and the baked
//     self-description manifest, both likewise performed by an external,
//     unmodeled build service) — BuildImage's own Go implementation
//     cannot enforce it directly, only request it via this field and
//     document the requirement here for whatever implements it.
//
// # Per-provider semantics differ — the port must not assume one
//
// §19.1: "concurrent builds sharing one cache volume need an explicit
// concurrency story, and per-provider semantics differ enough that the
// port must not assume one." This struct is the whole of that story at the
// port level: a plain key plus a plain path list, no lock handle, no
// provider-specific volume/mount object. HOW an adapter turns Key/Paths
// into an actual mounted volume — and whether it can safely do so at all
// for a given (Key, concurrent-access) situation — is entirely that
// adapter's own decision (see internal/adapters/outbound/modal's own
// BuildImage for the one implementation that exists today: it declines
// the mount and transparently retries as a cold build the instant its own
// wire protocol reports cache trouble).
type CacheMount struct {
	// Key names the ONE persistent cache volume this build should mount —
	// domain/imagebuild.CacheVolumeKey(Base, RuntimeVersion). Opaque
	// outside whichever adapter mounts it; this package makes no claim
	// about its own format beyond "deterministic for a given
	// (Base, RuntimeVersion) pair."
	Key string

	// Paths is the package-manager-agnostic, fixed, closed set of
	// well-known cache directories to mount at Key inside the build
	// sandbox — domain/imagebuild.WellKnownCachePaths, see that var's own
	// doc comment for the full list and why it stays fixed rather than
	// guessing which ecosystem(s) a given repo set actually uses.
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
