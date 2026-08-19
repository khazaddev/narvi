// This file (cloudidentityconfig.go) implements Step 73b's own ("cloud
// identity: sandbox-side consumption + kubeconfig injection", §27.3/§27.4)
// sandbox-agent-side client half of CP's POST /sessions/{id}/
// cloud-identity-config delivery endpoint (internal/adapters/inbound/
// httpapi/cloudidentityconfigdelivery.go) -- mirrors
// opencodeconfig.go/sandboxsecrets.go's own shape.

package credentials

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// maxCloudIdentityConfigResponseSize bounds how much of a CP
// cloud-identity-config response body is ever read -- mirrors
// maxOpenCodeConfigResponseSize/maxSandboxSecretsResponseSize's own
// identical reasoning.
const maxCloudIdentityConfigResponseSize = 1 << 20 // 1 MiB

// CloudIdentityConfigBinding is one resolved cloud_identity_bindings
// winner's own sandbox-agent-side decoded shape -- mirrors httpapi's own
// (unexported) cloudIdentityConfigBindingResponse.
type CloudIdentityConfigBinding struct {
	Kind     string          `json:"kind"`
	Audience string          `json:"audience"`
	Params   json.RawMessage `json:"params"`
}

// CloudIdentityConfigCluster is the resolved cluster_bindings row's own
// sandbox-agent-side decoded shape, when one exists -- mirrors httpapi's
// own (unexported) cloudIdentityConfigClusterResponse.
type CloudIdentityConfigCluster struct {
	Name      string          `json:"name"`
	ServerURL *string         `json:"serverUrl,omitempty"`
	CaBundle  *string         `json:"caBundle,omitempty"`
	AuthKind  string          `json:"authKind"`
	Params    json.RawMessage `json:"params"`
}

// CloudIdentityConfigDelivery is the sandbox-agent-side decoded shape of
// CP's cloud-identity-config delivery response.
type CloudIdentityConfigDelivery struct {
	Bindings       []CloudIdentityConfigBinding `json:"bindings"`
	ClusterBinding *CloudIdentityConfigCluster  `json:"clusterBinding,omitempty"`
}

// CloudIdentityConfigFetcher is the minimal interface boot-time code needs
// from a CP client -- CPClient satisfies it, and a test can supply a
// small fake with no real HTTP round trip. Mirrors
// OpenCodeConfigFetcher/SandboxSecretsFetcher's own identical shape.
type CloudIdentityConfigFetcher interface {
	FetchCloudIdentityConfig(ctx context.Context, sessionID, sandboxToken string, gen int) (CloudIdentityConfigDelivery, error)
}

// FetchCloudIdentityConfig POSTs (no body) to <baseURL>/sessions/
// <sessionID>/cloud-identity-config with the SAME two headers every other
// delivery call in this package sends, and expects back
// {"bindings":[...], "clusterBinding": {...}|omitted} on a 2xx response.
// bindings may legitimately be empty (no cloud identity binding
// configured for this session at all) and clusterBinding may legitimately
// be absent (no cluster configured for this session's own Environment) --
// neither is an error of its own.
//
// This METHOD itself makes exactly ONE HTTP attempt and applies no retry
// of its own -- any non-2xx response is returned as a *DeliveryStatusError
// (deliverystatus.go), any transport/decode failure as a plain wrapped
// error. §27.1's own "with bounded retry" requirement (applied identically
// here, per §27.2/opencodeconfig.go's own established "same handshake"
// framing) is implemented by this method's CALLER
// (cmd/sandbox-agent/cloudidentity.go's fetchCloudIdentityConfig), which
// wraps repeated calls to this method in platform.Retry using
// DeliveryStatusError.StatusCode to retry a transport error or a 5xx but
// never a 401/403/404/410 (this endpoint's own terminal handshake fences)
// -- mirrors FetchSandboxSecrets/FetchOpenCodeConfig's own identical
// design.
//
// The raw response body is deliberately never embedded in the returned
// error -- mirrors this package's own established rationale.
func (c CPClient) FetchCloudIdentityConfig(ctx context.Context, sessionID, sandboxToken string, gen int) (CloudIdentityConfigDelivery, error) {
	path := fmt.Sprintf("%s/sessions/%s/cloud-identity-config", c.baseURL, url.PathEscape(sessionID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, path, nil)
	if err != nil {
		return CloudIdentityConfigDelivery{}, fmt.Errorf("credentials: build cloud-identity-config request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+sandboxToken)
	req.Header.Set("X-Sandbox-Gen", strconv.Itoa(gen))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return CloudIdentityConfigDelivery{}, fmt.Errorf("credentials: cloud-identity-config request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCloudIdentityConfigResponseSize))
	if err != nil {
		return CloudIdentityConfigDelivery{}, fmt.Errorf("credentials: read cloud-identity-config response: %w", err)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// Deliberately does NOT include body -- see this func's own doc
		// comment above.
		return CloudIdentityConfigDelivery{}, &DeliveryStatusError{Endpoint: "cloud-identity-config", StatusCode: resp.StatusCode}
	}

	var parsed CloudIdentityConfigDelivery
	if err := json.Unmarshal(body, &parsed); err != nil {
		return CloudIdentityConfigDelivery{}, fmt.Errorf("credentials: decode cloud-identity-config response: %w", err)
	}
	return parsed, nil
}
