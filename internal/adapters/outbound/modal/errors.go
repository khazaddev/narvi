package modal

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"time"

	"github.com/khazaddev/narvi/internal/app/ports"
)

// --- Config errors (New) — named, structured, fail-fast, matching the
// established platform/config.go pattern. ---

// MissingConfigError is returned by New when a required Config field
// (BaseURL, AuthToken) is empty.
type MissingConfigError struct {
	Field string
}

func (e *MissingConfigError) Error() string {
	return fmt.Sprintf("modal: missing required Config.%s", e.Field)
}

// InvalidBaseURLError is returned by New when Config.BaseURL does not
// parse as an absolute URL (scheme + host). Value is redacted
// (redactURLCredentials) before New stores it here — BaseURL is not
// expected to carry credentials, but it costs nothing to close off the
// same leak class as InvalidEgressProxyURLError below.
type InvalidBaseURLError struct {
	Value string
}

func (e *InvalidBaseURLError) Error() string {
	return fmt.Sprintf("modal: invalid Config.BaseURL %q: must be an absolute URL", e.Value)
}

// InvalidEgressProxyURLError is returned by New when Config.EgressProxyURL
// is non-empty but does not parse as an absolute URL. The egress proxy is
// optional/fail-open (absent = direct connection); this error only fires
// when a value WAS supplied and it was malformed — never for an empty
// value.
//
// A proxy URL conventionally carries basic-auth credentials
// (http://user:pass@host:3128) — exactly what http.ProxyURL consumes in
// New. Value is redacted (redactURLCredentials) before New stores it
// here, so a malformed proxy URL never leaks its password into an error
// message that ends up logged or returned to a caller.
type InvalidEgressProxyURLError struct {
	Value string
}

func (e *InvalidEgressProxyURLError) Error() string {
	return fmt.Sprintf("modal: invalid Config.EgressProxyURL %q: must be an absolute URL", e.Value)
}

// credentialsPattern matches a leading "[scheme://]user:password@" prefix
// so it can be redacted regardless of whether the URL parsed cleanly —
// url.URL.Redacted only helps once url.Parse has already recognized a
// scheme and host, but the exact URLs these errors report are the ones
// that FAILED to parse that way (e.g. a missing "http://" scheme leaves
// the credentials sitting in url.URL.Opaque, which Redacted does not
// touch). Anchored at the start of the string: userinfo can only appear
// immediately after an optional scheme, never later in the URL, so this
// can't misfire on an "@" appearing in a path or query.
var credentialsPattern = regexp.MustCompile(`^(([a-zA-Z][a-zA-Z0-9+.-]*://)?[^/:@]*:)[^/@]*(@)`)

// redactURLCredentials replaces the password component of a leading
// userinfo prefix ("user:PASSWORD@...") with "xxxxx", leaving the rest of
// raw untouched. raw need not be a validly-parseable URL. A raw value
// with no userinfo prefix is returned unchanged.
func redactURLCredentials(raw string) string {
	return credentialsPattern.ReplaceAllString(raw, "${1}xxxxx${3}")
}

// ColdStartTimeoutError is returned by New when
// Config.Timeouts.ProviderHTTPClientTimeout does not exceed
// Config.Timeouts.ProviderWorstColdStart (§4.1: "The provider HTTP client
// timeout MUST exceed the provider's worst cold-start"). Under normal
// wiring (platform.DefaultTimeouts(), validated by platform.Timeouts.
// Validate() at control-plane boot) this can never fire — it is a
// defense-in-depth safety net for a caller-constructed Timeouts value.
type ColdStartTimeoutError struct {
	HTTPClientTimeout time.Duration
	WorstColdStart    time.Duration
}

func (e *ColdStartTimeoutError) Error() string {
	return fmt.Sprintf(
		"modal: Config.Timeouts.ProviderHTTPClientTimeout (%s) must exceed ProviderWorstColdStart (%s) — §4.1",
		e.HTTPClientTimeout, e.WorstColdStart,
	)
}

// errResumeUnsupported is the underlying error wrapped by the permanent
// ProviderError ResumeSandbox always returns — Modal reports
// Capabilities().Resume == false (§3.2: Modal is the snapshot-based
// provider, "restore = new gen", not the persistent-resume one).
var errResumeUnsupported = errors.New(
	"modal: ResumeSandbox is not supported; Capabilities().Resume is false",
)

// --- Response classification (the §4.1 crux) ---
//
// Classification table (never by string-matching the human message):
//
//	Network-level failure (timeout, connection refused, ...): Transient=true
//	HTTP 429 (Too Many Requests):                              Transient=true
//	HTTP 5xx:                                                   Transient=true
//	HTTP 400, 401, 403, 404, 409, 422:                          Transient=false (permanent)
//	Any other/unrecognized status:                              Transient=true
//	  (§3.2: "Unknown provider errors default to transient, never
//	  permanent — a novel transient failure must not trip the breaker.")

// isTransientStatus classifies an HTTP response status code per the table
// above. status is assumed to already be outside the 2xx success range.
func isTransientStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests:
		return true
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity:
		return false
	}
	if status >= http.StatusInternalServerError {
		return true
	}
	// Anything else — a status this table has no explicit entry for —
	// defaults to transient, never permanent (§3.2).
	return true
}

// classifyErrorResponse builds the ProviderError for a non-2xx HTTP
// response. It decodes body as a modalErrorBody best-effort purely to
// populate Code and a human-readable message for logging/debugging; the
// Transient decision itself comes ONLY from the HTTP status
// (isTransientStatus), never from the decoded code or message.
//
// The raw response body is deliberately NEVER embedded in the returned
// error: SESSION_CONFIG (§6.4) carries a plaintext sandbox bearer token
// (§5.2), and a validation-failure response is exactly the kind of body
// that can echo the request back verbatim. ProviderError.Error() is
// logged on every transition (§5.3), so anything placed in Err ends up in
// logs — only the provider's own decoded Message (or a fixed fallback
// string) ever goes there, never body's bytes.
func classifyErrorResponse(op ports.Op, status int, body []byte) *ports.ProviderError {
	code := fmt.Sprintf("http_%d", status)
	message := "no error body"

	var parsed modalErrorBody
	if err := json.Unmarshal(body, &parsed); err == nil {
		if parsed.Error.Code != "" {
			code = parsed.Error.Code
		}
		if parsed.Error.Message != "" {
			message = parsed.Error.Message
		} else if len(body) > 0 {
			message = "error body did not match the expected error envelope"
		}
	} else if len(body) > 0 {
		message = "error body was not valid JSON"
	}

	return &ports.ProviderError{
		Transient: isTransientStatus(status),
		Code:      code,
		Op:        op,
		Err:       fmt.Errorf("modal: http %d: %s", status, message),
	}
}

// classifyNetworkError builds the ProviderError for a request that never
// got an HTTP response at all (transport-level failure: timeout,
// connection refused, DNS failure, ...). §4.1's table treats every
// network-level failure as transient — a transport hiccup is exactly the
// kind of failure retry-with-backoff exists for. The Code distinguishes
// "timeout" (via the structural net.Error.Timeout() check — no string
// matching) from every other transport failure for logging purposes
// only; it never changes the Transient verdict, which is always true
// here.
func classifyNetworkError(op ports.Op, err error) *ports.ProviderError {
	code := "NETWORK_ERROR"
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		code = "NETWORK_TIMEOUT"
	}
	return &ports.ProviderError{
		Transient: true,
		Code:      code,
		Op:        op,
		Err:       err,
	}
}
