package credentials

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// maxScmCredentialsResponseSize bounds how much of a CP scm-credentials
// response body is ever read -- mirrors
// internal/adapters/outbound/modal/client.go's own maxResponseBodySize
// reasoning: http.Client.Timeout bounds wall-clock time, not response
// size, so an unbounded read could exhaust memory against a misbehaving
// server.
const maxScmCredentialsResponseSize = 1 << 20 // 1 MiB

// CredentialFetcher is the minimal interface Get/RunGet need from a CP
// client: CPClient (below) satisfies it, and tests can supply a small fake
// without any real HTTP round trip. Kept as a separate name from the
// concrete CPClient struct itself (both are valid per this Step's own
// "plain struct value or a tiny interface, your call" latitude) so the
// real HTTP-backed implementation and the narrow surface Get actually
// depends on stay independently readable.
//
// gen (audit remediation, security-crosscutting & docs-completeness-vs-plan
// lenses) is the sandbox's own current spawn generation (§3.2 fencing),
// sourced from the SAME SessionConfig.Gen the credential-helper subcommand
// already reads sessionID/sandboxToken from -- threaded through so CP's
// scm-credentials endpoint can reject a stale gen's request exactly like
// the sandbox WS handshake already does (internal/adapters/inbound/wshub/
// sandbox.go's own X-Sandbox-Gen check).
//
// forceReadOnly (§30.4(2)) is true when this sandbox is booting in
// BootModeBuild -- "a build only needs read". It can only narrow what CP
// hands back, never widen it: CP's own egress-mode resolution runs
// independently either way, so a sandbox that lies about its own boot
// mode gains nothing by omitting this.
type CredentialFetcher interface {
	Fetch(ctx context.Context, sessionID, sandboxToken string, gen int, host string, forceReadOnly bool) (Credential, error)
}

// CPClient is the credential helper's CP client: it POSTs to control
// plane's own /sessions/{id}/scm-credentials endpoint (§5.2) to mint a
// fresh git credential for one host.
//
// THE CP ENDPOINT THIS TALKS TO DOES NOT EXIST YET. §9.3's own "e2e happy
// path" work is what actually builds it.
// Exactly like §4.1 invented a documented, tested-against-a-fake-server
// wire contract for Modal, this file invents a plausible, documented
// request/response shape here and tests CPClient against a fake
// httptest.Server standing in for the real thing -- whoever builds the
// real endpoint reconciles the two sides then.
type CPClient struct {
	baseURL    string
	httpClient *http.Client
}

// InvalidControlPlaneWsURLError is returned by NewCPClient when
// controlPlaneWsUrl does not parse as a URL, or its scheme is not one of
// ws/wss.
type InvalidControlPlaneWsURLError struct {
	Value string
	Err   error
}

func (e *InvalidControlPlaneWsURLError) Error() string {
	return fmt.Sprintf("credentials: invalid controlPlaneWsUrl %q: %v", e.Value, e.Err)
}

func (e *InvalidControlPlaneWsURLError) Unwrap() error { return e.Err }

// NewCPClient derives baseURL from controlPlaneWsUrl (SessionConfig.
// ControlPlaneWsUrl, a wss://.../sessions/{id}/ws?type=sandbox URL, §6.1)
// by swapping the scheme (wss->https, ws->http) and keeping only the
// host -- there is no separate REST base URL field in SESSION_CONFIG
// (contracts/session-config/v1/session-config.schema.json is NOT touched
// by this Step), so this is a documented, reasoned derivation, not
// something the contract itself pins.
func NewCPClient(controlPlaneWsURL string, timeout time.Duration) (CPClient, error) {
	parsed, err := url.Parse(controlPlaneWsURL)
	if err != nil {
		return CPClient{}, &InvalidControlPlaneWsURLError{Value: controlPlaneWsURL, Err: err}
	}

	var httpScheme string
	switch parsed.Scheme {
	case "wss":
		httpScheme = "https"
	case "ws":
		// Plaintext ws:// carries the sandbox bearer token and the minted
		// credential response in the clear -- allowed ONLY when the host is
		// loopback (this package's own httptest-backed tests all point at
		// 127.0.0.1). A real control plane must use wss:// so this channel
		// is never interceptable/tamperable in flight; anything else
		// amplifies the CRLF-injection defense above from "requires a
		// compromised CP" to "requires only network position."
		if !isLoopbackHost(parsed.Host) {
			return CPClient{}, &InvalidControlPlaneWsURLError{
				Value: controlPlaneWsURL,
				Err:   errors.New("plaintext ws:// is only allowed for a loopback host (127.0.0.1/::1/localhost); a real control plane must use wss://"),
			}
		}
		httpScheme = "http"
	default:
		return CPClient{}, &InvalidControlPlaneWsURLError{
			Value: controlPlaneWsURL,
			Err:   fmt.Errorf("unrecognized scheme %q, want ws or wss", parsed.Scheme),
		}
	}

	return CPClient{
		baseURL:    httpScheme + "://" + parsed.Host,
		httpClient: &http.Client{Timeout: timeout},
	}, nil
}

// isLoopbackHost reports whether hostport's host component (a bare
// hostname, or "host:port") is loopback -- "localhost", or an IP for which
// net.IP.IsLoopback() is true (127.0.0.0/8, ::1). Used only to decide
// whether a derived plaintext ws->http scheme is safe (see NewCPClient
// above) -- never for anything security-load-bearing beyond that.
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

// scmCredentialsRequest is this Step's own invented, documented request
// shape POSTed to CP's future scm-credentials endpoint. protocol is
// always https by construction (credentials.Get refuses anything else
// before Fetch is ever called), so it is not sent.
//
// ForceReadOnly (§30.4(2)) mirrors CredentialFetcher's own doc comment
// above -- omitted (rather than sent false) when not set, matching every
// other boolean wire field in this codebase's own established convention.
type scmCredentialsRequest struct {
	Host          string `json:"host"`
	ForceReadOnly bool   `json:"forceReadOnly,omitempty"`
}

// scmCredentialsResponse is this Step's own invented, documented response
// shape expected back on a 2xx.
type scmCredentialsResponse struct {
	Username  string    `json:"username"`
	Password  string    `json:"password"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Fetch POSTs {"host": host} to <baseURL>/sessions/<sessionID>/
// scm-credentials with "Authorization: Bearer <sandboxToken>" and an
// "X-Sandbox-Gen: <gen>" header (mirroring internal/sandboxagent/wsbridge/
// run.go's own exact header name/convention for the sandbox WS handshake's
// gen-fencing header), and expects back {"username": "...", "password":
// "...", "expiresAt": "<RFC3339>"} on a 2xx response. Any non-2xx or
// malformed response is a plain error -- no retry, no transient/permanent
// classification (a single failed fetch is exactly what triggers Get's
// fail-closed "never fall back to stale cache" behavior, §5.2).
//
// The raw response body is deliberately NEVER embedded in the returned
// error: mirrors the exact lesson from the Modal adapter's own
// classifyErrorResponse (internal/adapters/outbound/modal/errors.go) --
// a validation-failure response is exactly the kind of body that can echo
// request/secret data back verbatim, and this error can end up logged.
// Only the HTTP status and a generic, fixed description ever surface.
func (c CPClient) Fetch(ctx context.Context, sessionID, sandboxToken string, gen int, host string, forceReadOnly bool) (Credential, error) {
	reqBody, err := json.Marshal(scmCredentialsRequest{Host: host, ForceReadOnly: forceReadOnly})
	if err != nil {
		return Credential{}, fmt.Errorf("credentials: encode scm-credentials request: %w", err)
	}

	path := fmt.Sprintf("%s/sessions/%s/scm-credentials", c.baseURL, url.PathEscape(sessionID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(reqBody))
	if err != nil {
		return Credential{}, fmt.Errorf("credentials: build scm-credentials request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sandboxToken)
	req.Header.Set("X-Sandbox-Gen", strconv.Itoa(gen))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Credential{}, fmt.Errorf("credentials: scm-credentials request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxScmCredentialsResponseSize))
	if err != nil {
		return Credential{}, fmt.Errorf("credentials: read scm-credentials response: %w", err)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// Deliberately does NOT include body -- see this func's own doc
		// comment above.
		return Credential{}, fmt.Errorf(
			"credentials: scm-credentials request for host %q returned http %d", host, resp.StatusCode,
		)
	}

	var parsed scmCredentialsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Credential{}, fmt.Errorf("credentials: decode scm-credentials response: %w", err)
	}
	if parsed.Username == "" || parsed.Password == "" {
		return Credential{}, errors.New("credentials: scm-credentials response missing username/password")
	}
	// RunGet writes Username/Password verbatim into git's own newline-
	// delimited credential-helper protocol (`username=...\n`,
	// `password=...\n`). A CP response (or a MITM on a plaintext channel --
	// see the loopback-only guard on plaintext ws:// in NewCPClient above)
	// that smuggled a "\n" into either field could inject an extra
	// key=value line git's own parser would then honor -- e.g. a second
	// "username=evil" line silently overriding the intended one, since git
	// applies fields in the order it reads them. Reject outright rather
	// than stripping: a password isn't safe to silently mutate, and a
	// rejection here correctly triggers Get's fail-closed "never fall back
	// to stale cache" behavior.
	if strings.ContainsAny(parsed.Username, "\r\n") || strings.ContainsAny(parsed.Password, "\r\n") {
		return Credential{}, errors.New(
			"credentials: scm-credentials response contains a newline in username/password",
		)
	}

	return Credential(parsed), nil
}
