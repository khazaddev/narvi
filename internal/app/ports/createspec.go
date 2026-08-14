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
// volume, one it simply cannot reach right now, or a hang/unparseable
// response from whatever mounted it — MUST silently decline the mount and
// perform an ordinary cold build instead, exactly as if CacheMount had
// been nil. A caller (app/imagebuild.Builder) must never be able to
// distinguish "the cache was used" from "the cache was declined" from
// BuildImage's return value alone — both are success. Every adapter whose
// Capabilities().ImageBuilds is false (RWX today, §4.1.1) never even
// reaches this field; RWX's own content-addressed layer cache already
// gives it this effect natively.
//
// # Key: Base + RuntimeVersion + rotation epoch — never repo content
//
// Key is domain/imagebuild.CacheVolumeKey(spec.Base, spec.RuntimeVersion,
// epoch) — deliberately excludes Repos, so every ImageSpec sharing the
// same Base and RuntimeVersion (and rotation epoch) resolves to the SAME
// cache volume regardless of which repo set it is building (one shared
// cache across every Environment built from the same base image and
// runtime, not one per Fingerprint). A cache keyed on repo content would
// recreate the exact cold start this design exists to remove: the very
// first build of a brand-new Environment would get zero benefit from
// every OTHER Environment's already-warmed dependency downloads. epoch
// (platform.Config.CacheVolumeEpoch, an operator-controlled value never
// baked into Fingerprint — see CacheVolumeKey's own doc comment) is the
// rotation escape hatch: it lets a stuck or oversized cache volume be
// abandoned for a fresh one WITHOUT bumping RuntimeVersion, which would
// otherwise also invalidate every shared IMAGE fleet-wide (§19.1's own
// "simultaneous-invalidation cliff") purely to solve a problem confined to
// the cache.
//
// # No lock — because nothing writes while a build can see this volume
//
// §19.2's refresh pump makes concurrent BuildImage calls against the SAME
// cache Key routine, not rare: it rebuilds every ready shared image
// whenever its repos' tips move, and nothing serializes two different
// fingerprints that happen to share a Base/RuntimeVersion/epoch triple.
// This port deliberately takes NO lock — neither here nor in any caller —
// around concurrent access to one cache volume.
//
// An earlier draft of this design justified that with the paths under
// WellKnownCachePaths being "content-addressed" — same package version in,
// identical bytes out, so a concurrent write could only ever collide with
// an identical one. That claim was checked against each package manager's
// own real, documented on-disk layout and found FALSE for nearly every
// mounted path (domain/imagebuild.WellKnownCachePaths' own doc comment has
// the full, per-tool findings — Go's module cache ships its OWN lock file
// for exactly this reason, npm/pip key their caches by request URL rather
// than response content and mutate in place, Gradle's metadata cache is
// guarded by a lock living INSIDE the mounted directory, and so on).
// Worse, mounting a directory carrying a tool's own host-local lock file
// across sandboxes on DIFFERENT hosts hands that tool a FALSE sense of
// mutual exclusion it cannot actually provide.
//
// The no-lock decision itself still stands — but on a different basis:
// every path in Paths is mounted READ-ONLY for the entire duration of the
// build. Nothing writes into the shared, persistent volume while a build
// can observe it, so there is no interleaving left to reason about, and a
// tool's own host-local advisory lock becomes irrelevant because there is
// nothing left for it to guard — concurrent corruption is impossible BY
// CONSTRUCTION, not by an argument about filename schemes. Exactly ONE
// write-back happens, merging whatever a build newly produced, and only
// AFTER that build has itself succeeded — never before, and never for a
// build that failed, preserving §19.1's original "writes committed only
// after success" invariant unchanged. This is also the literal
// implementation of what §19.1 already asked for and this design had not
// yet built: "writes committed only after a successful build" was
// asserted in prose but expressed nowhere in the port shape or the wire
// request before this; read-only-during-build plus a single post-success
// write-back is that sentence, taken literally, rather than an argument
// about the writes package managers happen to make.
//
// A package manager that needs to WRITE during the build (nearly all of
// them do, at least for a newly-resolved dependency) is given a private,
// per-build writable layer at these same logical paths — an ordinary
// read-through/write-back cache shape whose exact mechanism (a
// copy-on-write overlay, a seeded scratch copy, ...) is the adapter's or
// build service's own concern, external to this repository, exactly as
// the mount itself and the dependency install already are (this
// codebase's own Go code performs no such write itself). Every writer
// still MUST rely on atomic rename semantics for its OWN writable layer
// (write to a temp path in the same directory, then rename into place) —
// that discipline was never about inter-build safety in the first place,
// it is what keeps a single build's own concurrent readers (multiple
// package-manager processes inside the SAME build) from observing a
// partially-written file, and it stays required for that reason alone.
//
// One residual question this port does not resolve, and does not need to:
// whether a package manager tolerates its cache directory literally
// appearing read-only, absent that writable layer. Most of the eleven
// tools WellKnownCachePaths names support either an explicit read-only/
// no-cache mode or a separately configurable writable location (pip's
// documented `--no-cache-dir`, Go's documented `GOCACHE=off`, ...); at
// least one — Go's own module cache — is NOT documented to degrade
// gracefully on its own, which is exactly why the writable layer above is
// load-bearing rather than a nice-to-have: it makes every tool's own
// read-only tolerance moot by never letting any of them observe read-only
// in the first place.
//
// # Size is unbounded — a named gap, not a solved one
//
// Nothing in this design bounds, evicts, or expires anything in a cache
// volume, and one volume is shared fleet-wide across every Environment
// with the same Base/RuntimeVersion/epoch — so, left alone, it grows until
// the provider's own storage quota is hit and an in-sandbox dependency
// install starts failing with ENOSPC. No enforcement ships in this Step;
// the rotation epoch above is today's only escape hatch (mint a fresh,
// empty volume; the old one is simply abandoned, never explicitly
// deleted — a provider-side GC/TTL policy on unreferenced volumes, if the
// provider offers one, is the nearest thing to automatic reclamation
// today). Real enforcement — a hard byte cap, LRU eviction inside the
// volume, or both — is deliberately deferred to a later Step, named here
// explicitly rather than left implicit, mirroring §19.2's own "newly
// urgent, still deferred" image-GC posture.
//
// # Per-provider semantics differ — the port must not assume one
//
// §19.1: "concurrent builds sharing one cache volume need an explicit
// concurrency story, and per-provider semantics differ enough that the
// port must not assume one." This struct is the whole of that story at the
// port level: a plain key plus a plain path list, no lock handle, no
// provider-specific volume/mount object. HOW an adapter turns Key/Paths
// into an actual read-only-during-build, write-back-on-success mounted
// volume — and whether it can safely do so at all for a given Key — is
// entirely that adapter's own decision (see internal/adapters/outbound/
// modal's own BuildImage for the one implementation that exists today: it
// declines the mount and transparently retries as a cold build the
// instant its own wire protocol reports cache trouble — now broadened to
// also cover a transport-level hang and an unparseable response, not only
// three structured error codes; see modal/errors.go's isCacheMountTrouble
// for the full, current set).
type CacheMount struct {
	// Key names the ONE persistent cache volume this build should mount —
	// domain/imagebuild.CacheVolumeKey(Base, RuntimeVersion, epoch).
	// Opaque outside whichever adapter mounts it; this package makes no
	// claim about its own format beyond "deterministic for a given
	// (Base, RuntimeVersion, epoch) triple."
	Key string

	// Paths is the package-manager-agnostic, fixed, closed set of
	// well-known cache directories to mount at Key inside the build
	// sandbox, READ-ONLY for the duration of the build (see this struct's
	// own "No lock" section above) — domain/imagebuild.WellKnownCachePaths(),
	// see that function's own doc comment for the full list and why it
	// stays fixed rather than guessing which ecosystem(s) a given repo set
	// actually uses. A fresh slice on every call (never a shared backing
	// array a caller could accidentally mutate across builds).
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
