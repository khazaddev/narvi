package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// maxResponseBodySize bounds how much of an OpenCode response body ever
// gets read — mirroring internal/adapters/outbound/modal's own
// maxResponseBodySize precedent exactly (a misbehaving server must not
// exhaust memory by streaming an unbounded body over an otherwise-slow-
// but-alive connection). Sized generously above modal's 1 MiB since a
// message-list fallback fetch (GET /session/{id}/message) can carry
// sizeable tool output/text content.
const maxResponseBodySize = 4 << 20 // 4 MiB

// doJSON executes one OpenCode HTTP call: JSON-encodes reqBody when
// non-nil, sends it, and — on a 2xx response — JSON-decodes the body into
// out (when out is non-nil and the body is non-empty). Unlike
// internal/adapters/outbound/modal's own `do` helper, failures here are
// plain wrapped errors, not a typed *ports.ProviderError — AgentRuntime
// (unlike SandboxProvider) has no Transient/permanent classification
// contract of its own (§4.2's interface has no such requirement), so there
// is nothing this Step's own scope asks this adapter to classify errors
// into.
//
// Wraps ctx in a per-request context.WithTimeout(a.requestTimeout) before
// building the request (Finding 3) — every doJSON-routed caller
// (resolveSession, resolveModel, postPromptAsync, postAbort,
// fetchFinalMessages) is protected uniformly this way, without each call
// site needing to remember to wrap its own context. Most critically,
// fetchFinalMessages is called ONLY from finalizeByFallback — i.e. exactly
// when the SSE-inactivity fallback has already concluded something is
// wrong and needs a definitive answer; without this bound, a hung TCP
// connection on THAT call specifically could wedge an already-stuck turn
// forever. Deliberately a per-request wrap here, NOT a client-wide
// a.httpClient.Timeout: connectAndConsume's own GET /event call (sse.go)
// uses this SAME a.httpClient for the intentionally long-lived persistent
// SSE stream, which does NOT go through doJSON and so is correctly left
// unaffected by this timeout. context.WithTimeout already takes the
// tighter of a.requestTimeout and whatever deadline ctx might already
// carry, with no special-casing needed for that interaction.
func (a *Adapter) doJSON(ctx context.Context, method, path string, reqBody, out any) error {
	ctx, cancel := context.WithTimeout(ctx, a.requestTimeout)
	defer cancel()

	var bodyReader io.Reader
	if reqBody != nil {
		encoded, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("opencode: encode %s %s request: %w", method, path, err)
		}
		bodyReader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("opencode: build %s %s request: %w", method, path, err)
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("opencode: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		return fmt.Errorf("opencode: %s %s: read response body: %w", method, path, err)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("opencode: %s %s: http %d", method, path, resp.StatusCode)
	}

	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("opencode: %s %s: decode response: %w", method, path, err)
		}
	}
	return nil
}
