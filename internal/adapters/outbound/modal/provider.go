package modal

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// Provider is the Modal SandboxProvider adapter: it implements
// ports.SandboxProvider entirely by making HTTP calls to Modal's API
// through httpClient.
type Provider struct {
	baseURL    string
	authToken  string
	httpClient *http.Client
}

// var _ ports.SandboxProvider = (*Provider)(nil) makes a SandboxProvider
// signature drift a build error, not a runtime surprise.
var _ ports.SandboxProvider = (*Provider)(nil)

// New constructs a Provider from cfg. It validates cfg fail-fast (named
// errors, matching platform/config.go's established pattern) and — when
// cfg.EgressProxyURL is set — wires the constructed *http.Client's
// Transport to route every request through that proxy (§4.1: "All Modal
// traffic goes through the configurable egress proxy").
func New(cfg Config) (*Provider, error) {
	if cfg.BaseURL == "" {
		return nil, &MissingConfigError{Field: "BaseURL"}
	}
	parsedBase, err := url.Parse(cfg.BaseURL)
	if err != nil || parsedBase.Scheme == "" || parsedBase.Host == "" {
		return nil, &InvalidBaseURLError{Value: redactURLCredentials(cfg.BaseURL)}
	}

	if cfg.AuthToken == "" {
		return nil, &MissingConfigError{Field: "AuthToken"}
	}

	if cfg.Timeouts.ProviderHTTPClientTimeout <= cfg.Timeouts.ProviderWorstColdStart {
		return nil, &ColdStartTimeoutError{
			HTTPClientTimeout: cfg.Timeouts.ProviderHTTPClientTimeout,
			WorstColdStart:    cfg.Timeouts.ProviderWorstColdStart,
		}
	}

	transport := &http.Transport{}
	if cfg.EgressProxyURL != "" {
		parsedProxy, err := url.Parse(cfg.EgressProxyURL)
		if err != nil || parsedProxy.Scheme == "" || parsedProxy.Host == "" {
			return nil, &InvalidEgressProxyURLError{Value: redactURLCredentials(cfg.EgressProxyURL)}
		}
		transport.Proxy = http.ProxyURL(parsedProxy)
	}

	return &Provider{
		baseURL:   strings.TrimSuffix(cfg.BaseURL, "/"),
		authToken: cfg.AuthToken,
		httpClient: &http.Client{
			Timeout:   cfg.Timeouts.ProviderHTTPClientTimeout,
			Transport: transport,
		},
	}, nil
}

// Capabilities reports Modal's real capability set (§3.2, §4.1): Modal
// snapshots and builds images, allows an explicit stop, but is the
// snapshot-based provider ("restore = new gen") rather than a
// persistent-resume one.
//
// DockerInSandbox/EgressPolicy report true (§27.5/§27.6, Step 74): Modal
// maps CreateSpec.Docker onto its own VM runtime option and
// CreateSpec.EgressPolicy onto its own sandbox network controls (see
// runtimeForSpec/networkPolicyFromSpec below) — both real, substrate-
// level capabilities this adapter actually implements, not aspirational
// flags. See CreateSandbox's own doc comment for §27.8's own genuinely
// unresolved point this reporting deliberately does NOT paper over:
// Snapshots being reported unconditionally true here does not, by
// itself, claim snapshot parity for a Docker-enabled (VM-runtime)
// sandbox — that is resolved separately, at the call site that decides
// whether to ever attempt a snapshot at all (app/sessionactor's own
// triggerSnapshotBestEffort, sandboxevent.go), not by this method
// pretending the two runtimes are identical.
func (p *Provider) Capabilities() ports.Capabilities {
	return ports.Capabilities{
		Snapshots:       true,
		Resume:          false,
		ExplicitStop:    true,
		ImageBuilds:     true,
		DockerInSandbox: true,
		EgressPolicy:    true,
	}
}

// CreateSandbox POSTs spec to /v1/sandboxes, with the full SESSION_CONFIG
// document nested under "sessionConfig" as one JSON blob (§4.1).
//
// spec.Docker/spec.EgressPolicy (§27.5/§27.6, Step 74) are mapped onto
// Modal's own Runtime/NetworkPolicy wire fields via runtimeForSpec/
// networkPolicyFromSpec, read from spec's OWN top-level duplicate
// fields — never from spec.SessionConfig.Docker/EgressPolicy, even
// though spec.Validate() (just below) already proved the two agree: this
// is exactly the point of the top-level duplication (§27.5's own "the
// provider must act on it without parsing the opaque doc" — CreateSpec's
// own doc comment). §27.5's own named, real costs of the VM-runtime
// option (boot latency against §19's warm-boot expectations, its
// experimental status, per-sandbox cost) are not modeled anywhere in
// this adapter's own wire protocol — there is no cost/latency field to
// set — they are a deployment-level tradeoff a customer accepts by
// setting the flag at all, not something this adapter's own request
// shape can express or mitigate.
func (p *Provider) CreateSandbox(ctx context.Context, spec ports.CreateSpec) (ports.SandboxRef, error) {
	if err := spec.Validate(); err != nil {
		return ports.SandboxRef{}, &ports.ProviderError{Transient: false, Code: "INVALID_SPEC", Op: ports.OpCreateSandbox, Err: err}
	}
	req := createSandboxRequest{
		Gen:           spec.Gen,
		Image:         spec.Image,
		SessionConfig: spec.SessionConfig,
		Runtime:       runtimeForSpec(spec.Docker),
		NetworkPolicy: networkPolicyFromSpec(spec.EgressPolicy),
	}
	var resp sandboxResponse
	if err := p.do(ctx, ports.OpCreateSandbox, http.MethodPost, "/v1/sandboxes", req, &resp); err != nil {
		return ports.SandboxRef{}, err
	}
	return ports.SandboxRef{ProviderID: resp.SandboxID}, nil
}

// StopSandbox POSTs to /v1/sandboxes/{id}/stop.
func (p *Provider) StopSandbox(ctx context.Context, ref ports.SandboxRef) error {
	path := "/v1/sandboxes/" + url.PathEscape(ref.ProviderID) + "/stop"
	return p.do(ctx, ports.OpStopSandbox, http.MethodPost, path, nil, nil)
}

// ResumeSandbox always fails: Modal reports Capabilities().Resume ==
// false (§3.2 — Modal is the snapshot-based provider, not the
// persistent-resume one), so this never makes an HTTP call at all — a
// caller that ignores Capabilities and calls it anyway gets a permanent,
// typed ProviderError instead of a network round trip, a silent no-op, or
// a panic.
func (p *Provider) ResumeSandbox(_ context.Context, _ ports.SandboxRef) error {
	return &ports.ProviderError{
		Transient: false,
		Code:      "UNSUPPORTED_OPERATION",
		Op:        ports.OpResumeSandbox,
		Err:       errResumeUnsupported,
	}
}

// TakeSnapshot POSTs to /v1/sandboxes/{id}/snapshot.
func (p *Provider) TakeSnapshot(ctx context.Context, ref ports.SandboxRef) (ports.SnapshotID, error) {
	path := "/v1/sandboxes/" + url.PathEscape(ref.ProviderID) + "/snapshot"
	var resp snapshotResponse
	if err := p.do(ctx, ports.OpTakeSnapshot, http.MethodPost, path, nil, &resp); err != nil {
		return "", err
	}
	return ports.SnapshotID(resp.SnapshotID), nil
}

// RestoreFromSnapshot POSTs to /v1/sandboxes/restore, again carrying the
// full SESSION_CONFIG document as one JSON blob (§4.1) alongside the
// snapshot id and new gen.
func (p *Provider) RestoreFromSnapshot(ctx context.Context, id ports.SnapshotID, spec ports.CreateSpec) (ports.SandboxRef, error) {
	if err := spec.Validate(); err != nil {
		return ports.SandboxRef{}, &ports.ProviderError{Transient: false, Code: "INVALID_SPEC", Op: ports.OpRestoreFromSnapshot, Err: err}
	}
	req := restoreSandboxRequest{
		SnapshotID:    string(id),
		Gen:           spec.Gen,
		Image:         spec.Image,
		SessionConfig: spec.SessionConfig,
		Runtime:       runtimeForSpec(spec.Docker),
		NetworkPolicy: networkPolicyFromSpec(spec.EgressPolicy),
	}
	var resp sandboxResponse
	if err := p.do(ctx, ports.OpRestoreFromSnapshot, http.MethodPost, "/v1/sandboxes/restore", req, &resp); err != nil {
		return ports.SandboxRef{}, err
	}
	return ports.SandboxRef{ProviderID: resp.SandboxID}, nil
}

// BuildImage POSTs to /v1/images.
//
// # Cache-mount decline-and-fall-back-to-cold-build (§19.1's closing
// # paragraph, Step 43(c), third iteration: immutable versioned cache
// # snapshots)
//
// When spec.CacheMount is set, the first attempt carries it as
// req.CacheVolume — requesting spec.CacheMount.MountVersion mounted
// READ-ONLY for this build (empty MountVersion = nothing to mount yet,
// this key's first build) and spec.CacheMount.PublishVersion as the new,
// distinct, immutable version this build's own outputs publish under if it
// succeeds (ports.CacheMount's own doc comment has the full contract; no
// separate wire field says "this write is safe" the way an earlier
// draft's read-write design needed one to — MountVersion and
// PublishVersion always naming two DIFFERENT, individually-immutable
// objects is what makes the write safe, not a flag). If that attempt
// fails with a *ports.ProviderError isCacheMountTrouble (errors.go)
// recognizes as ambiguous enough to blame on the cache — a structured
// cache-trouble code (corruption, unavailability, a build-service-
// reported internal timeout, or MountVersion not found/already pruned) or
// an unparseable response on an otherwise-transient status; never a raw
// client-side transport timeout (see isCacheMountTrouble's own doc
// comment for why that signal was removed rather than kept), and never an
// ordinary, recognized build failure — BuildImage retries EXACTLY ONCE
// with CacheVolume dropped entirely — an ordinary cold build,
// indistinguishable on the wire from a request that never asked for a
// cache mount in the first place. This is this adapter's own concrete
// implementation of the decline permission ports.CacheMount's own doc
// comment grants every adapter: the caller (app/imagebuild.Builder) never
// sees a cache-specific error and never needs to special-case one — a
// corrupted, unavailable, hung, not-found, or unparseable-response cache
// costs one extra HTTP round trip here, never a BuildImage failure. A
// failure on the SECOND (cold) attempt is a genuine build failure,
// unrelated to the cache, and is returned exactly as any other BuildImage
// failure — no special handling, same retry/backoff path through
// app/imagebuild.Builder's own recordFailure as always.
//
// BuildOutcome.PublishedCacheVersion is set to req.CacheVolume.
// PublishVersion ONLY when the EVENTUAL successful attempt's own request
// still carried CacheVolume (i.e. it was never dropped by the fallback
// above) — empty otherwise, including when spec.CacheMount was nil to
// begin with. This is what lets app/imagebuild.Builder tell "a real
// publish happened" from "the mount was silently declined" without
// BuildImage ever growing a cache-specific error (BuildOutcome's own doc
// comment has the full reasoning).
func (p *Provider) BuildImage(ctx context.Context, spec ports.ImageSpec) (ports.BuildOutcome, error) {
	var repos map[string]imageBuildRequestRepo
	if len(spec.Repos) > 0 {
		repos = make(map[string]imageBuildRequestRepo, len(spec.Repos))
		for name, ref := range spec.Repos {
			repos[name] = imageBuildRequestRepo{URL: ref.URL, SHA: ref.SHA}
		}
	}
	req := imageBuildRequest{
		Base:           spec.Base,
		Repos:          repos,
		RuntimeVersion: spec.RuntimeVersion,
		CacheVolume:    cacheVolumeFromSpec(spec.CacheMount),
	}

	var resp buildResponse
	err := p.do(ctx, ports.OpBuildImage, http.MethodPost, "/v1/images", req, &resp)
	if err != nil && req.CacheVolume != nil && isCacheMountTrouble(err) {
		platform.Logger(ctx).Warn("modal: BuildImage: cache mount unavailable; retrying as an ordinary cold build (pure-accelerator fallback, §19.1)",
			"cache_key", req.CacheVolume.Key, "mount_version", req.CacheVolume.MountVersion, "error", err)
		req.CacheVolume = nil
		err = p.do(ctx, ports.OpBuildImage, http.MethodPost, "/v1/images", req, &resp)
	}
	if err != nil {
		return ports.BuildOutcome{}, err
	}
	outcome := ports.BuildOutcome{Ref: ports.BuildRef(resp.BuildID)}
	if req.CacheVolume != nil {
		outcome.PublishedCacheVersion = req.CacheVolume.PublishVersion
	}
	return outcome, nil
}

// runtimeGVisor/runtimeVM are createSandboxRequest/restoreSandboxRequest.
// Runtime's own closed two-value vocabulary (§27.5, Step 74).
// runtimeGVisor is Modal's own default gVisor sandbox — deliberately the
// EMPTY string, so a Docker-false request omits Runtime from the wire
// entirely (json:",omitempty") rather than sending an explicit
// "gvisor" Modal never asked for; this is what keeps a Docker-false
// request byte-for-byte identical to what this adapter sent before this
// field existed. runtimeVM requests Modal's own VM-runtime sandbox
// option (§27.5's "Modal concretely": "Modal's VM runtime option gives
// the sandbox a real kernel, where Docker/compose/build behave
// normally").
//
// There is deliberately no third value, and never will be: §27.5 is
// explicit that privileged-mode DinD is rejected outright, under every
// configuration — there is no privileged runtime option in this
// adapter's own closed vocabulary for runtimeForSpec to ever select.
const (
	runtimeGVisor = ""
	runtimeVM     = "vm"
)

// runtimeForSpec maps ports.CreateSpec.Docker onto this adapter's own
// closed Runtime vocabulary — an exhaustive, two-case function that
// cannot produce a third, privileged value (runtimeGVisor/runtimeVM's
// own doc comment).
func runtimeForSpec(dockerRequired bool) string {
	if dockerRequired {
		return runtimeVM
	}
	return runtimeGVisor
}

// networkPolicyFromSpec translates a ports.CreateSpec.EgressPolicy into
// this adapter's own wire shape, or returns nil when policy is nil (no
// egress policy attached to this Environment) — mirroring
// cacheVolumeFromSpec's own identical "nil in, nil out, byte-for-byte
// unaffected" shape immediately below.
func networkPolicyFromSpec(policy *sessionconfig.SessionConfigEgressPolicy) *networkPolicyWire {
	if policy == nil {
		return nil
	}
	return &networkPolicyWire{
		Mode:      string(policy.Mode),
		Allowlist: policy.Allowlist,
	}
}

// cacheVolumeFromSpec translates a ports.CacheMount into this adapter's own
// wire shape, or returns nil when mount is nil (no cache requested) —
// keeping the "no CacheMount -> byte-for-byte identical request as before
// this field existed" property wire.go's own imageBuildRequest.CacheVolume
// doc comment promises. Key, MountVersion, and PublishVersion all travel
// verbatim — see imageBuildRequestCacheVolume's own doc comment for the
// third-iteration MountVersion/PublishVersion fields.
func cacheVolumeFromSpec(mount *ports.CacheMount) *imageBuildRequestCacheVolume {
	if mount == nil {
		return nil
	}
	return &imageBuildRequestCacheVolume{
		Key:            mount.Key,
		MountVersion:   mount.MountVersion,
		PublishVersion: mount.PublishVersion,
		Paths:          mount.Paths,
	}
}

// DeleteImage issues a DELETE to /v1/images/{id}.
func (p *Provider) DeleteImage(ctx context.Context, ref ports.ImageRef) error {
	path := "/v1/images/" + url.PathEscape(string(ref))
	return p.do(ctx, ports.OpDeleteImage, http.MethodDelete, path, nil, nil)
}

// List GETs /v1/sandboxes for reconciliation/orphan GC.
func (p *Provider) List(ctx context.Context) ([]ports.SandboxRef, error) {
	var resp listResponse
	if err := p.do(ctx, ports.OpList, http.MethodGet, "/v1/sandboxes", nil, &resp); err != nil {
		return nil, err
	}
	refs := make([]ports.SandboxRef, 0, len(resp.Sandboxes))
	for _, s := range resp.Sandboxes {
		refs = append(refs, ports.SandboxRef{ProviderID: s.SandboxID})
	}
	return refs, nil
}
