// This file (cloudidentitytoken.go) implements Step 73b's own ("cloud
// identity: sandbox-side consumption + kubeconfig injection", §27.3)
// sandbox-agent-side client half of Step 73a's own CP minting endpoint,
// POST /sessions/{id}/cloud-identity-token (internal/adapters/inbound/
// httpapi/cloudidentitytoken.go) -- mirrors sandboxsecrets.go/
// opencodeconfig.go's own shape, adapted for a request BODY (the
// audience) that those two endpoints do not have.
//
// Two callers, both boot/refresh-time and both best-effort (never fatal
// to boot, §27.1's own posture extended to §27.3 -- see
// cmd/sandbox-agent/cloudidentity.go's own doc comment):
//   - cmd/sandbox-agent/cloudidentity.go's own token-file population/
//     refresh loop, once per resolved binding.
//   - cmd/sandbox-agent's own kube-credential subcommand (main.go,
//     runKubeCredentialHelper), for the AuthKindOIDC cluster rung -- a
//     fresh mint on EVERY invocation, since client-go's own exec-plugin
//     cache already avoids over-invoking based on the returned
//     expirationTimestamp (see that subcommand's own doc comment).

package credentials

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// maxCloudIdentityTokenResponseSize bounds how much of a CP
// cloud-identity-token response body is ever read -- mirrors this
// package's own established sizing precedent for a small, fixed-shape
// JSON document.
const maxCloudIdentityTokenResponseSize = 1 << 16 // 64 KiB

// MintedCloudIdentityToken is the sandbox-agent-side decoded shape of a
// successful mint response -- Token is the signed JWT; ExpiresAt is its
// own `exp` claim, rendered as a real time.Time (never logged or
// otherwise surfaced alongside Token -- mirrors this codebase's own
// "never log token material" discipline throughout).
type MintedCloudIdentityToken struct {
	Token     string
	ExpiresAt time.Time
}

// CloudIdentityTokenMinter is the minimal interface boot/refresh-time
// code needs from a CP client -- CPClient satisfies it, and a test can
// supply a small fake with no real HTTP round trip. Mirrors this
// package's own established narrow-interface-alongside-the-concrete-struct
// shape.
type CloudIdentityTokenMinter interface {
	MintCloudIdentityToken(ctx context.Context, sessionID, sandboxToken string, gen int, audience string) (MintedCloudIdentityToken, error)
}

// mintCloudIdentityTokenRequest mirrors httpapi's own (unexported)
// mintCloudIdentityTokenRequest wire shape exactly.
type mintCloudIdentityTokenRequest struct {
	Audience string `json:"audience"`
}

// mintCloudIdentityTokenResponse mirrors httpapi's own (unexported)
// mintCloudIdentityTokenResponse wire shape exactly.
type mintCloudIdentityTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// MintCloudIdentityToken POSTs {"audience": audience} to <baseURL>/
// sessions/<sessionID>/cloud-identity-token with the SAME two headers
// every other delivery call in this package sends, and expects back
// {"token": "...", "expiresAt": "<RFC3339>"} on a 2xx response.
//
// This METHOD itself makes exactly ONE HTTP attempt and applies no retry
// of its own -- any non-2xx response is returned as a *DeliveryStatusError
// (deliverystatus.go; notably including 503 -- §27.3's own fail-closed
// "capability off" / "no active signing key" fence, NOT a generic
// transient-server-error status here, see deliveryretry.go's own
// classifyMintTokenError for how a caller must treat it differently from
// every OTHER delivery endpoint's own 503-as-retryable default), any
// transport/decode failure as a plain wrapped error. Bounded retry is
// this method's CALLER's own job (cmd/sandbox-agent/cloudidentity.go's
// mintCloudIdentityToken, and the kube-credential subcommand), exactly
// like every other Fetch*/Mint* method in this package.
//
// The raw response body is deliberately never embedded in the returned
// error -- mirrors this package's own established rationale. The
// audience VALUE itself is not secret (it is public, customer-configured
// binding metadata -- internal/domain/cloudidentity.ValidateAudience's
// own doc comment), so it is safe to include in the request body/log
// context a caller builds around this call; only the returned Token
// itself must never be logged.
func (c CPClient) MintCloudIdentityToken(ctx context.Context, sessionID, sandboxToken string, gen int, audience string) (MintedCloudIdentityToken, error) {
	reqBody, err := json.Marshal(mintCloudIdentityTokenRequest{Audience: audience})
	if err != nil {
		return MintedCloudIdentityToken{}, fmt.Errorf("credentials: encode cloud-identity-token request: %w", err)
	}

	path := fmt.Sprintf("%s/sessions/%s/cloud-identity-token", c.baseURL, url.PathEscape(sessionID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(reqBody))
	if err != nil {
		return MintedCloudIdentityToken{}, fmt.Errorf("credentials: build cloud-identity-token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sandboxToken)
	req.Header.Set("X-Sandbox-Gen", strconv.Itoa(gen))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return MintedCloudIdentityToken{}, fmt.Errorf("credentials: cloud-identity-token request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCloudIdentityTokenResponseSize))
	if err != nil {
		return MintedCloudIdentityToken{}, fmt.Errorf("credentials: read cloud-identity-token response: %w", err)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// Deliberately does NOT include body -- see this func's own doc
		// comment above.
		return MintedCloudIdentityToken{}, &DeliveryStatusError{Endpoint: "cloud-identity-token", StatusCode: resp.StatusCode}
	}

	var parsed mintCloudIdentityTokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return MintedCloudIdentityToken{}, fmt.Errorf("credentials: decode cloud-identity-token response: %w", err)
	}
	if parsed.Token == "" {
		return MintedCloudIdentityToken{}, fmt.Errorf("credentials: cloud-identity-token response missing token")
	}
	return MintedCloudIdentityToken(parsed), nil
}
