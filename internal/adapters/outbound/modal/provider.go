package modal

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/khazaddev/narvi/internal/app/ports"
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
func (p *Provider) Capabilities() ports.Capabilities {
	return ports.Capabilities{
		Snapshots:    true,
		Resume:       false,
		ExplicitStop: true,
		ImageBuilds:  true,
	}
}

// CreateSandbox POSTs spec to /v1/sandboxes, with the full SESSION_CONFIG
// document nested under "sessionConfig" as one JSON blob (§4.1).
func (p *Provider) CreateSandbox(ctx context.Context, spec ports.CreateSpec) (ports.SandboxRef, error) {
	if err := spec.Validate(); err != nil {
		return ports.SandboxRef{}, &ports.ProviderError{Transient: false, Code: "INVALID_SPEC", Op: ports.OpCreateSandbox, Err: err}
	}
	req := createSandboxRequest{
		Gen:           spec.Gen,
		Image:         spec.Image,
		SessionConfig: spec.SessionConfig,
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
	}
	var resp sandboxResponse
	if err := p.do(ctx, ports.OpRestoreFromSnapshot, http.MethodPost, "/v1/sandboxes/restore", req, &resp); err != nil {
		return ports.SandboxRef{}, err
	}
	return ports.SandboxRef{ProviderID: resp.SandboxID}, nil
}

// BuildImage POSTs to /v1/images.
func (p *Provider) BuildImage(ctx context.Context, spec ports.ImageSpec) (ports.BuildRef, error) {
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
	}
	var resp buildResponse
	if err := p.do(ctx, ports.OpBuildImage, http.MethodPost, "/v1/images", req, &resp); err != nil {
		return "", err
	}
	return ports.BuildRef(resp.BuildID), nil
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
