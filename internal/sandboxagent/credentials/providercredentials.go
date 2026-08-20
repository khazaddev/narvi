// This file (providercredentials.go) implements §25.1's own ("provider
// credential injection", §25.1/§25.3) sandbox-agent-side client half of
// CP's POST /sessions/{id}/provider-credentials delivery endpoint
// (internal/adapters/inbound/httpapi/providercredentialsdelivery.go) --
// the counterpart to cpclient.go's own Fetch (the SCM/git-credential
// case), reconciled against that REAL, already-implemented CP endpoint
// (unlike Fetch's own history, this Step's CP side was built first, in
// the same batch, so there is no "invented, then reconciled later" gap
// here).
//
// Deliberately a SECOND METHOD on the SAME CPClient (cpclient.go), not a
// sibling client type: CPClient's only real job is "the sandbox's own
// authenticated HTTP channel to CP, derived once from SessionConfig.
// ControlPlaneWsUrl" -- baseURL/httpClient/NewCPClient's own ws->http
// derivation are exactly what THIS call needs too, and nothing about them
// is SCM-specific. The one call-shape difference from Fetch is that this
// call needs no "host" parameter at all (there is no per-host git remote
// concept for a provider API key) -- it takes only the same
// sessionID/sandboxToken/gen triple Fetch already does. A second,
// independently-constructed client type would duplicate NewCPClient's own
// URL-derivation/loopback-scheme-guard logic for zero real benefit.

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

// maxProviderCredentialsResponseSize bounds how much of a CP provider-
// credentials response body is ever read -- mirrors
// maxScmCredentialsResponseSize's own identical reasoning (this file's
// sibling, cpclient.go): http.Client.Timeout bounds wall-clock time, not
// response size. The real payload here is at most 3 short API keys, so
// this is a generous ceiling, never a tuned value.
const maxProviderCredentialsResponseSize = 1 << 20 // 1 MiB

// ProviderCredentialsFetcher is the minimal interface spawn-time code
// needs from a CP client -- CPClient satisfies it, and a test can supply a
// small fake with no real HTTP round trip. Mirrors CredentialFetcher's own
// identical "narrow interface alongside the concrete struct" shape
// (cpclient.go).
type ProviderCredentialsFetcher interface {
	FetchProviderCredentials(ctx context.Context, sessionID, sandboxToken string, gen int) (map[string]AuthValue, error)
}

// AuthValue mirrors internal/adapters/inbound/httpapi's own (unexported)
// credentialAuthValue byte-for-byte (§29.6) -- independently
// declared here, reconciled by hand, exactly like providerCredentials
// Response's own pre-existing doc comment already established for this
// sibling endpoint (scmcredentials.go's identical precedent). Has
// structurally NO field for a refresh token, in either variant -- see
// that same doc comment's full chain for why this is deliberate, not an
// oversight: the CP delivery response this type decodes never carries
// one either.
type AuthValue struct {
	Type      string  `json:"type"`
	Key       *string `json:"key,omitempty"`
	Access    *string `json:"access,omitempty"`
	Expires   *int64  `json:"expires,omitempty"`
	AccountID *string `json:"accountId,omitempty"`
}

// providerCredentialsResponse mirrors internal/adapters/inbound/httpapi's
// own (unexported) providerCredentialsResponse -- a map from provider name
// to its resolved AuthValue (§29.6 -- a plain plaintext-string
// map before this Step).
type providerCredentialsResponse struct {
	Credentials map[string]AuthValue `json:"credentials"`
}

// FetchProviderCredentials POSTs (no body) to
// <baseURL>/sessions/<sessionID>/provider-credentials with
// "Authorization: Bearer <sandboxToken>" and an "X-Sandbox-Gen: <gen>"
// header -- the SAME two headers Fetch already sends, mirroring that
// method's own exact header-name/convention choices -- and expects back
// {"credentials": {"<provider>": {"type": "api"|"oauth", ...}, ...}} on a
// 2xx response (§29.6). A provider with nothing configured for
// this session is simply ABSENT from the map, never an error of its own;
// the map itself may legitimately be empty (the overwhelming common case:
// no provider credential configured at any scope for this session).
//
// Any non-2xx or malformed response is a plain error -- no retry, no
// transient/permanent classification, mirroring Fetch's own identical
// "a single failed fetch is exactly what triggers fail-closed behavior"
// design. UNLIKE Fetch, though, a failed call here is NOT itself fatal to
// the caller's own larger operation: see cmd/sandbox-agent/main.go's own
// call site doc comment for why a fetch failure degrades to "spawn
// `opencode serve` with today's unchanged, ambient environment" rather
// than aborting the boot -- that leniency is a decision for the CALLER to
// make, not something this method itself should silently do by
// swallowing an error.
//
// The raw response body is deliberately never embedded in the returned
// error -- mirrors Fetch's own identical rationale (a validation-failure
// body is exactly the kind of thing that can echo request data back
// verbatim, and this error can end up logged).
func (c CPClient) FetchProviderCredentials(ctx context.Context, sessionID, sandboxToken string, gen int) (map[string]AuthValue, error) {
	path := fmt.Sprintf("%s/sessions/%s/provider-credentials", c.baseURL, url.PathEscape(sessionID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, path, nil)
	if err != nil {
		return nil, fmt.Errorf("credentials: build provider-credentials request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+sandboxToken)
	req.Header.Set("X-Sandbox-Gen", strconv.Itoa(gen))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("credentials: provider-credentials request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderCredentialsResponseSize))
	if err != nil {
		return nil, fmt.Errorf("credentials: read provider-credentials response: %w", err)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// Deliberately does NOT include body -- see this func's own doc
		// comment above.
		return nil, fmt.Errorf("credentials: provider-credentials request returned http %d", resp.StatusCode)
	}

	var parsed providerCredentialsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("credentials: decode provider-credentials response: %w", err)
	}
	return parsed.Credentials, nil
}
