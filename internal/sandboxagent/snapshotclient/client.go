// Package snapshotclient implements sandbox-agent's own client for the
// control plane's snapshot-mint endpoint (Step 22, "snapshots & restore",
// design decision 2: POST /sessions/{id}/snapshot). Mirrors internal/
// sandboxagent/credentials.CPClient's own base-URL-derivation pattern
// exactly (that package's own cpclient.go) -- a small, analogous client,
// not a copy-paste of that git-credential-specific one: no disk cache, no
// git-credential-helper protocol, no descriptor parsing -- just a single
// bounded POST carrying a sandbox bearer token, returning a real
// snapshotId.
//
// cmd/sandbox-agent/main.go's real HandleSnapshot (design decision 4) is
// this package's only caller: it calls New(cfg.SessionConfig.
// ControlPlaneWsUrl, timeouts.SnapshotMintTimeout), then Mint(ctx,
// cfg.SessionConfig.SessionId, cfg.SessionConfig.SandboxToken,
// cfg.SessionConfig.Gen) to obtain a real snapshotId before reporting it
// back over the WS bridge as a CRITICAL "snapshot_ready" event. On any
// failure obtaining the id, HandleSnapshot logs and returns without
// sending anything -- design decision 2's own honest, documented
// limitation: no NACK-shaped event exists on the wire to report a failure
// here back to the control plane.
//
// Audit remediation (security-crosscutting lens): Mint's own gen parameter
// was added alongside the control-plane's own snapshot-mint endpoint
// (internal/adapters/inbound/httpapi.SnapshotMint) gaining a mandatory
// X-Sandbox-Gen check -- mirroring internal/sandboxagent/credentials.
// CPClient.Fetch's own identical gen parameter/header exactly. Without this
// client-side change, every real production snapshot-mint request would
// start failing that new server-side check with 403.
package snapshotclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// maxResponseSize bounds how much of a CP snapshot-mint response body is
// ever read -- mirrors internal/sandboxagent/credentials/cpclient.go's own
// maxScmCredentialsResponseSize reasoning: http.Client.Timeout bounds
// wall-clock time, not response size, so an unbounded read could exhaust
// memory against a misbehaving server.
const maxResponseSize = 1 << 20 // 1 MiB

// Client is sandbox-agent's own snapshot-mint client: it POSTs to control
// plane's own /sessions/{id}/snapshot endpoint (design decision 2) to
// mint a real snapshot id for the session's own live sandbox.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// InvalidControlPlaneWsURLError is returned by New when controlPlaneWsUrl
// does not parse as a URL, or its scheme is not one of ws/wss -- mirrors
// internal/sandboxagent/credentials.InvalidControlPlaneWsURLError exactly
// (same derivation, same loopback-only-plaintext-ws rule; duplicated
// rather than exported cross-package for such a small, self-contained
// type -- see New's own doc comment).
type InvalidControlPlaneWsURLError struct {
	Value string
	Err   error
}

func (e *InvalidControlPlaneWsURLError) Error() string {
	return fmt.Sprintf("snapshotclient: invalid controlPlaneWsUrl %q: %v", e.Value, e.Err)
}

func (e *InvalidControlPlaneWsURLError) Unwrap() error { return e.Err }

// New derives baseURL from controlPlaneWsUrl (SessionConfig.
// ControlPlaneWsUrl, a wss://.../sessions/{id}/ws?type=sandbox URL, §6.1)
// by swapping the scheme (wss->https, ws->http) and keeping only the
// host -- the SAME derivation internal/sandboxagent/credentials.
// NewCPClient already uses, duplicated here rather than exported
// cross-package for such a small, dependency-free derivation (that
// package's own cpclient.go doc comment gives the identical reasoning for
// its own analogous duplications).
func New(controlPlaneWsURL string, timeout time.Duration) (Client, error) {
	parsed, err := url.Parse(controlPlaneWsURL)
	if err != nil {
		return Client{}, &InvalidControlPlaneWsURLError{Value: controlPlaneWsURL, Err: err}
	}

	var httpScheme string
	switch parsed.Scheme {
	case "wss":
		httpScheme = "https"
	case "ws":
		// Plaintext ws:// carries the sandbox bearer token and the minted
		// snapshot id in the clear -- allowed ONLY when the host is
		// loopback (this package's own tests all point at 127.0.0.1),
		// mirroring credentials.NewCPClient's own identical rule exactly.
		if !isLoopbackHost(parsed.Host) {
			return Client{}, &InvalidControlPlaneWsURLError{
				Value: controlPlaneWsURL,
				Err:   errors.New("plaintext ws:// is only allowed for a loopback host (127.0.0.1/::1/localhost); a real control plane must use wss://"),
			}
		}
		httpScheme = "http"
	default:
		return Client{}, &InvalidControlPlaneWsURLError{
			Value: controlPlaneWsURL,
			Err:   fmt.Errorf("unrecognized scheme %q, want ws or wss", parsed.Scheme),
		}
	}

	return Client{
		baseURL:    httpScheme + "://" + parsed.Host,
		httpClient: &http.Client{Timeout: timeout},
	}, nil
}

// isLoopbackHost mirrors internal/sandboxagent/credentials/cpclient.go's
// own function of the same name exactly.
func isLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// mintResponse is the wire shape expected back on a 2xx -- mirrors
// internal/adapters/inbound/httpapi's own (unexported)
// snapshotMintResponse exactly, deliberately, not coincidentally (same
// reconciliation discipline scmCredentialsResponse/CPClient's own
// scmCredentialsResponse already established for each other).
type mintResponse struct {
	SnapshotID string `json:"snapshotId"`
}

// Mint POSTs to <baseURL>/sessions/<sessionID>/snapshot with
// "Authorization: Bearer <sandboxToken>" and an "X-Sandbox-Gen: <gen>"
// header (mirroring internal/sandboxagent/wsbridge/run.go's own exact
// header name/convention for the sandbox WS handshake's gen-fencing
// header, and credentials.CPClient.Fetch's own identical addition for the
// scm-credentials endpoint) and no body, and expects back
// {"snapshotId": "..."} on a 2xx response. Any non-2xx or malformed
// response is a plain error -- no retry (design decision 2's own honest,
// documented limitation: no NACK-shaped event exists on the wire to
// report a failure here back to the control plane, so HandleSnapshot's
// own caller simply logs and gives up on any error this returns).
//
// The raw response body is deliberately NEVER embedded in the returned
// error -- mirrors credentials.CPClient.Fetch's own identical discipline
// (a validation-failure response could echo request/secret data back
// verbatim, and this error can end up logged).
func (c Client) Mint(ctx context.Context, sessionID, sandboxToken string, gen int) (string, error) {
	path := fmt.Sprintf("%s/sessions/%s/snapshot", c.baseURL, url.PathEscape(sessionID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, path, nil)
	if err != nil {
		return "", fmt.Errorf("snapshotclient: build snapshot-mint request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+sandboxToken)
	req.Header.Set("X-Sandbox-Gen", strconv.Itoa(gen))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("snapshotclient: snapshot-mint request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return "", fmt.Errorf("snapshotclient: read snapshot-mint response: %w", err)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// Deliberately does NOT include body -- see this func's own doc
		// comment above.
		return "", fmt.Errorf("snapshotclient: snapshot-mint request returned http %d", resp.StatusCode)
	}

	var parsed mintResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("snapshotclient: decode snapshot-mint response: %w", err)
	}
	if parsed.SnapshotID == "" {
		return "", errors.New("snapshotclient: snapshot-mint response missing snapshotId")
	}
	return parsed.SnapshotID, nil
}
