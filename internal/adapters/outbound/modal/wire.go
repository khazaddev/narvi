package modal

import "github.com/narvidev/narvi/contracts/gen/go/sessionconfig"

// This file holds the request/response JSON shapes this adapter sends to
// and expects back from Modal's API. As doc.go explains, there is no real
// Modal API reachable from this codebase — these shapes are this
// adapter's own invention, pinned by the tests in provider_test.go against
// a fake httptest.Server, not against real Modal API docs.
//
// The one hard requirement every shape below honors is §4.1's "sandbox
// env passed as one SESSION_CONFIG JSON document — the provider never
// assembles env fragments": every request that needs SESSION_CONFIG
// carries it as a single nested sessionConfig object, never spread across
// top-level fields.

// createSandboxRequest is the body POSTed to /v1/sandboxes.
//
// # No privileged-mode field, deliberately, permanently (§27.5)
//
// This struct's field set is closed by construction: Runtime is a
// two-value enum (runtimeGVisor's empty default, or runtimeVM — see
// runtimeForSpec) and NetworkPolicy only ever carries a mode +
// allowlist. Neither this struct, nor anything this adapter's own
// runtimeForSpec/networkPolicyFromSpec helpers build, has any field that
// could ask Modal for privileged-mode Docker-in-Docker — §27.5 is
// explicit that privileged DinD is "rejected outright here... not a
// default, not an option, not behind a flag." TestProvider_
// CreateSandbox_WireRequestNeverCarriesAPrivilegedField (provider_test.go)
// pins this structurally, via reflection over the actual JSON this
// adapter sends, not merely by this comment's own claim.
type createSandboxRequest struct {
	Gen           int                         `json:"gen"`
	Image         string                      `json:"image,omitempty"`
	SessionConfig sessionconfig.SessionConfig `json:"sessionConfig"`

	// Runtime maps ports.CreateSpec.Docker onto Modal's own VM-runtime
	// sandbox option (§27.5, "Modal concretely": "default Modal
	// sandboxes run on gVisor, where dockerd's overlay2/bridge-networking
	// stack does not run cleanly; Modal's VM runtime option gives the
	// sandbox a real kernel"). Empty (omitted from the wire entirely)
	// means Modal's own default gVisor sandbox — every request built
	// before this field existed, and every Docker-false request today,
	// is byte-for-byte unaffected. See runtimeForSpec.
	Runtime string `json:"runtime,omitempty"`

	// NetworkPolicy maps ports.CreateSpec.EgressPolicy onto Modal's own
	// sandbox network controls (§27.6). Nil (omitted from the
	// wire entirely) means no egress restriction requested — Modal's own
	// default open egress. See networkPolicyFromSpec.
	NetworkPolicy *networkPolicyWire `json:"networkPolicy,omitempty"`
}

// networkPolicyWire is createSandboxRequest/restoreSandboxRequest's own
// NetworkPolicy shape — mirrors ports.CreateSpec.EgressPolicy field-for-
// field, the same "invented, tested against a fake httptest.Server, not
// real Modal API docs" posture as every other shape in this file.
type networkPolicyWire struct {
	Mode      string   `json:"mode"`
	Allowlist []string `json:"allowlist,omitempty"`
}

// restoreSandboxRequest is the body POSTed to /v1/sandboxes/restore. Same
// Runtime/NetworkPolicy fields as createSandboxRequest, for the identical
// reason — a restore is still asking for a real, live sandbox instance,
// so it needs the same substrate mapping a fresh create does.
type restoreSandboxRequest struct {
	SnapshotID    string                      `json:"snapshotId"`
	Gen           int                         `json:"gen"`
	Image         string                      `json:"image,omitempty"`
	SessionConfig sessionconfig.SessionConfig `json:"sessionConfig"`
	Runtime       string                      `json:"runtime,omitempty"`
	NetworkPolicy *networkPolicyWire          `json:"networkPolicy,omitempty"`
}

// imageBuildRequest is the body POSTed to /v1/images. §19.1 ("warm boot:
// shared fingerprint", §19.1): repos now carries BOTH the clone url and
// the concrete sha per repo (imageBuildRequestRepo below), not a bare
// name->sha map — this is what lets the (external, opaque-to-this-repo)
// build service do a real, full clone from the real origin (§19.1: "build
// service bakes /narvi/image-manifest.json and full clones") instead of a
// SHA with no origin to fetch it from.
type imageBuildRequest struct {
	Base           string                           `json:"base"`
	Repos          map[string]imageBuildRequestRepo `json:"repos,omitempty"`
	RuntimeVersion string                           `json:"runtimeVersion,omitempty"`

	// CacheVolume, when present, requests the build-time dependency cache
	// (§19.1's closing paragraph(c); ports.ImageSpec.CacheMount's
	// own doc comment has the full contract). omitempty: a spec with no
	// CacheMount produces a request byte-for-byte identical to what this
	// adapter sent before this field existed — no behavior change for a
	// caller that never opts in. Dropped to nil (never re-sent) by
	// Provider.BuildImage's own cold-build retry the instant the fake
	// wire protocol reports cache trouble — see errors.go's
	// isCacheMountTrouble and provider.go's BuildImage.
	CacheVolume *imageBuildRequestCacheVolume `json:"cacheVolume,omitempty"`
}

// imageBuildRequestRepo is imageBuildRequest.Repos' own value shape,
// mirroring ports.RepoRef{URL, SHA} field-for-field.
type imageBuildRequestRepo struct {
	URL string `json:"url"`
	SHA string `json:"sha"`
}

// imageBuildRequestCacheVolume is imageBuildRequest.CacheVolume's own value
// shape, mirroring ports.CacheMount{Key, MountVersion, PublishVersion,
// Paths} field-for-field — same "invented, tested against a fake
// httptest.Server, not real Modal API docs" posture as every other shape
// in this file (see this file's own top doc comment). MountVersion/
// PublishVersion are §19.1(c)'s third iteration (immutable versioned
// cache snapshots, ports.CacheMount's own doc comment): MountVersion names
// the one already-published, immutable version to mount read-only (empty =
// nothing to mount yet, this cache key's first build); PublishVersion
// names the brand-new version this build's own outputs publish under if it
// succeeds. Both travel on the wire as plain strings — this adapter makes
// no claim about their format beyond "opaque, minted by
// app/imagebuild.Builder via internal/adapters/outbound/postgres.
// ImageCacheVersionStore".
type imageBuildRequestCacheVolume struct {
	Key            string   `json:"key"`
	MountVersion   string   `json:"mountVersion,omitempty"`
	PublishVersion string   `json:"publishVersion"`
	Paths          []string `json:"paths,omitempty"`
}

// sandboxResponse is returned by CreateSandbox and RestoreFromSnapshot on
// success.
type sandboxResponse struct {
	SandboxID string `json:"sandboxId"`
}

// snapshotResponse is returned by TakeSnapshot on success.
type snapshotResponse struct {
	SnapshotID string `json:"snapshotId"`
}

// buildResponse is returned by BuildImage on success.
type buildResponse struct {
	BuildID string `json:"buildId"`
}

// listResponse is returned by List on success.
type listResponse struct {
	Sandboxes []sandboxResponse `json:"sandboxes"`
}

// modalErrorBody is the error envelope this adapter expects Modal to
// return on non-2xx responses. Error.Code, when present, becomes
// ports.ProviderError.Code (for logging/debugging only — see errors.go:
// the transient/permanent CLASSIFICATION itself is driven by HTTP status,
// never by this code or by Error.Message).
type modalErrorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}
