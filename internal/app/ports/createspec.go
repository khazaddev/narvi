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
// interface"). §10 Phase 2 gives the intended fingerprinting policy
// ("fingerprint = repo SHAs + runtime version; always fall back to base
// image on any miss — never block a session") — that full scheduling
// workflow is a later Step; this Step only needs the fingerprint-able
// inputs a BuildImage call carries.
type ImageSpec struct {
	// Base is the base image reference to build from (a registry
	// tag/digest).
	Base string

	// RepoSHAs fingerprints the repo content baked into the image,
	// keyed by repo name, so a multi-repo session's image build is
	// reproducible from the same inputs the fingerprint hashes.
	RepoSHAs map[string]string

	// RuntimeVersion is the pinned toolchain/runtime version baked into
	// the image (part of the same fingerprint).
	RuntimeVersion string
}
