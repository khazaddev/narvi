package modal

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/narvidev/narvi/internal/app/ports"
	"github.com/narvidev/narvi/internal/platform"
)

// maxResponseBodySize bounds how much of a Modal response body ever gets
// read. http.Client.Timeout bounds wall-clock time, not response size —
// without this cap a misbehaving server could exhaust memory by
// streaming an unbounded body over an otherwise-slow-but-alive
// connection.
const maxResponseBodySize = 1 << 20 // 1 MiB

// do executes one Modal API call: builds the request (JSON-encoding
// reqBody when non-nil, exactly as one document — never spread across
// separate fields), attaches auth and (when present on ctx) the
// correlation id, sends it, and — on a 2xx response — JSON-decodes the
// body into out (when out is non-nil). Any failure, network-level or an
// HTTP error status, is returned as a classified *ports.ProviderError
// tagged with op.
func (p *Provider) do(ctx context.Context, op ports.Op, method, path string, reqBody, out any) error {
	var bodyReader io.Reader
	if reqBody != nil {
		encoded, err := json.Marshal(reqBody)
		if err != nil {
			return &ports.ProviderError{Transient: false, Code: "ENCODE_ERROR", Op: op, Err: err}
		}
		bodyReader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, bodyReader)
	if err != nil {
		return &ports.ProviderError{Transient: false, Code: "REQUEST_BUILD_ERROR", Op: op, Err: err}
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+p.authToken)
	if correlationID, ok := platform.CorrelationIDFromContext(ctx); ok && correlationID != "" {
		req.Header.Set(platform.CorrelationIDHeader, correlationID)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return classifyNetworkError(op, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		return &ports.ProviderError{Transient: true, Code: "RESPONSE_READ_ERROR", Op: op, Err: err}
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return classifyErrorResponse(op, resp.StatusCode, respBody)
	}

	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return &ports.ProviderError{Transient: true, Code: "DECODE_ERROR", Op: op, Err: err}
		}
	}

	return nil
}
