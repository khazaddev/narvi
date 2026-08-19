// This file (opencodeconfig.go) implements Step 72's own ("sandbox
// secrets & opencode config", §27.2) sandbox-agent-side client half of
// CP's POST /sessions/{id}/opencode-config delivery endpoint
// (internal/adapters/inbound/httpapi/opencodeconfigdelivery.go) --
// mirrors sandboxsecrets.go/providercredentials.go's own shape.

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

// maxOpenCodeConfigResponseSize bounds how much of a CP opencode-config
// response body is ever read -- mirrors
// maxSandboxSecretsResponseSize/maxProviderCredentialsResponseSize's own
// identical reasoning.
const maxOpenCodeConfigResponseSize = 1 << 20 // 1 MiB

// OpenCodeConfigDelivery is the sandbox-agent-side decoded shape of CP's
// opencode-config delivery response -- Global/Environment each nil when
// not configured for this session, mirroring
// internal/adapters/inbound/httpapi's own openCodeConfigDeliveryResponse.
type OpenCodeConfigDelivery struct {
	Global      json.RawMessage
	Environment json.RawMessage
}

// OpenCodeConfigFetcher is the minimal interface spawn-time code needs
// from a CP client -- CPClient satisfies it, and a test can supply a
// small fake with no real HTTP round trip. Mirrors
// SandboxSecretsFetcher/ProviderCredentialsFetcher's own identical shape.
type OpenCodeConfigFetcher interface {
	FetchOpenCodeConfig(ctx context.Context, sessionID, sandboxToken string, gen int) (OpenCodeConfigDelivery, error)
}

// openCodeConfigDeliveryResponse mirrors internal/adapters/inbound/
// httpapi's own (unexported) openCodeConfigDeliveryResponse wire shape.
type openCodeConfigDeliveryResponse struct {
	Global      json.RawMessage `json:"global,omitempty"`
	Environment json.RawMessage `json:"environment,omitempty"`
}

// FetchOpenCodeConfig POSTs (no body) to
// <baseURL>/sessions/<sessionID>/opencode-config with the SAME two
// headers every other delivery call in this package sends, and expects
// back {"global": {...}|omitted, "environment": {...}|omitted} on a 2xx
// response. Either or both documents may legitimately be absent (the
// overwhelming common case: no OpenCode config configured at either
// scope).
//
// Any non-2xx or malformed response is a plain error -- no retry, no
// transient/permanent classification, mirroring
// FetchSandboxSecrets/FetchProviderCredentials' own identical design. A
// failed call here is NOT itself fatal to the caller's own larger
// operation -- see cmd/sandbox-agent/main.go's own call site doc comment.
//
// The raw response body is deliberately never embedded in the returned
// error -- mirrors this package's own established rationale.
func (c CPClient) FetchOpenCodeConfig(ctx context.Context, sessionID, sandboxToken string, gen int) (OpenCodeConfigDelivery, error) {
	path := fmt.Sprintf("%s/sessions/%s/opencode-config", c.baseURL, url.PathEscape(sessionID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, path, nil)
	if err != nil {
		return OpenCodeConfigDelivery{}, fmt.Errorf("credentials: build opencode-config request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+sandboxToken)
	req.Header.Set("X-Sandbox-Gen", strconv.Itoa(gen))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return OpenCodeConfigDelivery{}, fmt.Errorf("credentials: opencode-config request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxOpenCodeConfigResponseSize))
	if err != nil {
		return OpenCodeConfigDelivery{}, fmt.Errorf("credentials: read opencode-config response: %w", err)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// Deliberately does NOT include body -- see this func's own doc
		// comment above.
		return OpenCodeConfigDelivery{}, fmt.Errorf("credentials: opencode-config request returned http %d", resp.StatusCode)
	}

	var parsed openCodeConfigDeliveryResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return OpenCodeConfigDelivery{}, fmt.Errorf("credentials: decode opencode-config response: %w", err)
	}
	return OpenCodeConfigDelivery(parsed), nil
}
