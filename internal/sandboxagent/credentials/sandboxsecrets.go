// This file (sandboxsecrets.go) implements Step 72's own ("sandbox
// secrets & opencode config", §27.1) sandbox-agent-side client half of
// CP's POST /sessions/{id}/sandbox-secrets delivery endpoint
// (internal/adapters/inbound/httpapi/sandboxsecretsdelivery.go) --
// mirrors providercredentials.go's own FetchProviderCredentials shape
// exactly, simplified to a bare name->value map (a sandbox secret is
// always a plain string, there is no oauth-kind discriminated union the
// way a provider credential can be, Step 59's §29.6).

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

// maxSandboxSecretsResponseSize bounds how much of a CP sandbox-secrets
// response body is ever read -- mirrors
// maxProviderCredentialsResponseSize's own identical reasoning: a
// generous, not-tuned ceiling, since http.Client.Timeout already bounds
// wall-clock time.
const maxSandboxSecretsResponseSize = 1 << 20 // 1 MiB

// SandboxSecretsFetcher is the minimal interface spawn-time code needs
// from a CP client -- CPClient satisfies it, and a test can supply a
// small fake with no real HTTP round trip. Mirrors
// ProviderCredentialsFetcher's own identical "narrow interface alongside
// the concrete struct" shape.
type SandboxSecretsFetcher interface {
	FetchSandboxSecrets(ctx context.Context, sessionID, sandboxToken string, gen int) (map[string]string, error)
}

// sandboxSecretsResponse mirrors internal/adapters/inbound/httpapi's own
// (unexported) sandboxSecretsResponse -- a plain map from secret name to
// its resolved plaintext value.
type sandboxSecretsResponse struct {
	Secrets map[string]string `json:"secrets"`
}

// FetchSandboxSecrets POSTs (no body) to
// <baseURL>/sessions/<sessionID>/sandbox-secrets with
// "Authorization: Bearer <sandboxToken>" and an "X-Sandbox-Gen: <gen>"
// header -- the SAME two headers Fetch/FetchProviderCredentials already
// send -- and expects back {"secrets": {"<name>": "<value>", ...}} on a
// 2xx response. A name with nothing configured for this session is
// simply ABSENT from the map, never an error of its own; the map itself
// may legitimately be empty (the overwhelming common case: no sandbox
// secret configured at any scope for this session).
//
// This METHOD itself makes exactly ONE HTTP attempt and applies no retry
// of its own -- any non-2xx response is returned as a *DeliveryStatusError
// (deliverystatus.go), any transport/decode failure as a plain wrapped
// error. §27.1's own "with bounded retry" requirement is implemented by
// this method's CALLER (cmd/sandbox-agent's fetchSandboxSecrets), which
// wraps repeated calls to this method in platform.Retry, using
// DeliveryStatusError.StatusCode to retry a transport error or a 5xx but
// never a 401/403/404/410 (this endpoint's own terminal handshake
// fences). UNLIKE Fetch (the git-credential-helper's own scm-credentials
// call, which is intentionally never retried -- §5.2's fail-closed
// posture), a failed call here is NOT itself fatal to the caller's own
// larger operation -- see cmd/sandbox-agent/main.go's own call site doc
// comment for why an exhausted retry still degrades to "boot with today's
// unchanged, ambient environment" rather than aborting the boot.
//
// The raw response body is deliberately never embedded in the returned
// error -- mirrors Fetch/FetchProviderCredentials' own identical
// rationale.
func (c CPClient) FetchSandboxSecrets(ctx context.Context, sessionID, sandboxToken string, gen int) (map[string]string, error) {
	path := fmt.Sprintf("%s/sessions/%s/sandbox-secrets", c.baseURL, url.PathEscape(sessionID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, path, nil)
	if err != nil {
		return nil, fmt.Errorf("credentials: build sandbox-secrets request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+sandboxToken)
	req.Header.Set("X-Sandbox-Gen", strconv.Itoa(gen))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("credentials: sandbox-secrets request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSandboxSecretsResponseSize))
	if err != nil {
		return nil, fmt.Errorf("credentials: read sandbox-secrets response: %w", err)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// Deliberately does NOT include body -- see this func's own doc
		// comment above. A typed *DeliveryStatusError (not a plain
		// fmt.Errorf) so the caller's own retry wrapper can classify this
		// by StatusCode alone -- see this func's own doc comment.
		return nil, &DeliveryStatusError{Endpoint: "sandbox-secrets", StatusCode: resp.StatusCode}
	}

	var parsed sandboxSecretsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("credentials: decode sandbox-secrets response: %w", err)
	}
	return parsed.Secrets, nil
}
