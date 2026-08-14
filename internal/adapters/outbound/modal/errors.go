package modal

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/khazaddev/narvi/internal/app/ports"
)

// networkErrorCode/networkTimeoutCode are classifyNetworkError's own two
// Code values (below), named as constants purely for classifyNetworkError's
// own internal consistency (never left as ad-hoc literals). Neither is
// consulted by isCacheMountTrouble — see that function's own doc comment
// for why a network-level failure (timeout or otherwise) is deliberately
// NOT one of its signals.
// httpCodePrefix is classifyErrorResponse's own fallback-Code prefix
// ("http_<status>"), used by isCacheMountTrouble to recognize "this
// response never decoded into a real, recognized code at all".
const (
	networkErrorCode   = "NETWORK_ERROR"
	networkTimeoutCode = "NETWORK_TIMEOUT"
	httpCodePrefix     = "http_"
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
	code := fmt.Sprintf("%s%d", httpCodePrefix, status)
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
	code := networkErrorCode
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		code = networkTimeoutCode
	}
	return &ports.ProviderError{
		Transient: true,
		Code:      code,
		Op:        op,
		Err:       err,
	}
}

// --- Cache-mount decline detection (§19.1's closing paragraph, Step 43(c)) ---

// cacheMountTroubleCodes is the fixed, closed set of STRUCTURED error codes
// this adapter's own invented wire protocol (doc.go: no real Modal
// account/API reachable from this codebase) uses to signal that a
// BuildImage call failed BECAUSE of the requested CacheVolume specifically
// — never because of the underlying build itself. This is the narrower of
// TWO signals isCacheMountTrouble below checks; see that function's own
// doc comment for the broader unparseable-response signal a structured-
// code-only check misses.
//
// Provider.BuildImage's own retry-without-cache fallback (provider.go)
// checks a failed attempt's decoded Code against exactly this set (never a
// string-matched message, §4.1) to decide whether to retry once,
// transparently, with CacheVolume dropped.
//
// This is a DIFFERENT question from isTransientStatus's own
// Transient/permanent table above: that table answers "should the CALLER
// (app/imagebuild.Builder) retry this fingerprint later, with backoff";
// this set answers "should THIS adapter itself retry, once, right now,
// without the cache" — a corrupted or unavailable cache volume is
// presented here as a permanent-status response (so a caller that ignored
// this adapter's own fallback and retried the ORIGINAL request with the
// SAME cache mount would not busy-loop against a condition that will not
// self-heal), but that permanent/transient status never governs whether
// THIS adapter falls back — membership in cacheMountTroubleCodes does,
// unconditionally, regardless of Transient.
//
// Deliberately a closed set rather than a single catch-all "any error
// while a cache mount was requested" heuristic: an ordinary build failure
// (e.g. setup.sh itself failing inside the build sandbox) can occur on a
// request that also happens to carry a cache mount, and must NOT be
// silently retried as "maybe it was the cache" — that would mask a real
// build defect behind an extra, slower, doomed-to-fail-identically retry.
// Only a code this adapter's own protocol reserves specifically for cache
// trouble, or the broader ambiguous-failure signal below, triggers the
// fallback.
//
// # No CACHE_MOUNT_LOCKED — a lock is meaningless under immutable versions
//
// Attempt 2 (the read-only-mount-plus-one-write-back design) reserved
// CACHE_MOUNT_LOCKED for exactly one scenario: another build's own
// unguarded write-back into the SAME shared, mutable volume this request
// was trying to read from. That scenario cannot occur under this
// (third) iteration's own immutable-version model — there is no shared
// mutable state left for anything to lock (ports.CacheMount's own "a lock
// is meaningless" doc comment has the full reasoning) — so keeping this
// code in this adapter's own vocabulary would silently contradict that
// design claim: nothing a conformant build service does under this
// contract could ever legitimately report it. Dropped, not merely
// unused.
var cacheMountTroubleCodes = map[string]bool{
	// CACHE_MOUNT_CORRUPTED: the specific version named by MountVersion
	// failed whatever integrity check the (external, unmodeled) build
	// service runs before handing it to a build.
	"CACHE_MOUNT_CORRUPTED": true,
	// CACHE_MOUNT_UNAVAILABLE: the cache subsystem could not be reached at
	// all for this build (e.g. the provider's own volume/storage
	// subsystem is degraded).
	"CACHE_MOUNT_UNAVAILABLE": true,
	// CACHE_MOUNT_TIMEOUT: the build service's OWN internal timeout on the
	// cache-mount step fired, and it reported that cleanly and fast —
	// distinct from, and this adapter's own honest replacement for, a
	// bare CLIENT-side transport timeout (see isCacheMountTrouble's own
	// doc comment for why a raw client timeout is deliberately NOT a
	// cache-trouble signal in this iteration, and why a well-behaved
	// build service reporting its OWN timeout structurally, well inside
	// the client's own ProviderHTTPClientTimeout budget, is the honest
	// way to represent "the cache subsystem hung" without that defect).
	"CACHE_MOUNT_TIMEOUT": true,
	// CACHE_VERSION_NOT_FOUND: the specific version named by MountVersion
	// no longer exists at the provider — e.g. this control plane's own
	// retention pruning (domain/imagebuild.PruneCacheVersions) had already
	// stopped offering it as a MountVersion candidate by the time some
	// OTHER, already-in-flight request named it, or the provider's own,
	// independent lifecycle reclaimed it. This is the concrete mechanism
	// behind ports.CacheMount's own "what a reader does if its pinned
	// version has been pruned" answer: exactly this decline-and-retry-cold
	// path, never a build failure.
	"CACHE_VERSION_NOT_FOUND": true,
}

// isCacheMountTrouble reports whether err is ambiguous enough, on a
// request that itself carried a CacheMount, to justify Provider.
// BuildImage's own one-shot cold-build retry (provider.go) — see that
// function's own doc comment for why the retry itself is always scoped to
// "this exact request asked for a cache mount", never a blanket retry of
// every failure. Two signals, folded into one boolean because provider.
// go's own call site only ever needs the yes/no answer:
//
//  1. A STRUCTURED code this adapter's own protocol reserves specifically
//     for cache trouble (cacheMountTroubleCodes above) — the narrowest,
//     most confident signal: the build service told us, in an
//     unambiguous, recognized vocabulary, that the cache itself was the
//     problem, including its OWN internal cache-mount timeout
//     (CACHE_MOUNT_TIMEOUT) reported cleanly and fast, well inside this
//     client's own ProviderHTTPClientTimeout budget.
//  2. An UNPARSEABLE or bodyless error response on an otherwise TRANSIENT
//     status (classifyErrorResponse's own "http_<status>" fallback Code,
//     used exactly when the body could not be decoded into this adapter's
//     own structured envelope, or decoded with an empty Code) — e.g. a
//     503 with a non-JSON body. Restricted to Transient statuses
//     deliberately: this is what keeps the guard against masking a
//     genuine build defect intact. A genuine build failure in this
//     adapter's own invented protocol always carries a real, recognized
//     code in a structured envelope (e.g. "SETUP_SCRIPT_FAILED") — never
//     this fallback shape — so signal 2 only ever fires on a response
//     this adapter cannot attribute to anything it recognizes, on a
//     status class already documented as retry-worthy. This signal costs
//     at most one extra HTTP round trip if it turns out to be wrong (a
//     genuine, unrelated defect that also happens to return a malformed
//     5xx): the SECOND attempt runs cold, and if the underlying problem
//     persists, it surfaces there as an ordinary BuildImage failure,
//     unmasked — never silently retried forever.
//
// # Network-level failures (classifyNetworkError's own NETWORK_ERROR and
// # NETWORK_TIMEOUT codes) are deliberately NOT a signal here — narrowed,
// # not merely renamed
//
// An earlier draft of this adapter treated a transport-level failure —
// most importantly NETWORK_TIMEOUT — as cache trouble, on the reasoning
// that "a degraded cache-volume subsystem typically hangs rather than
// returning a clean error." That broadening was itself a defect, not a
// fix, and the clearest way to see why is the timeout case specifically:
// a request that already timed out has, by definition, already consumed
// this client's ENTIRE platform.Timeouts.ProviderHTTPClientTimeout
// budget — for a reason this adapter cannot distinguish from the outside,
// since neither NETWORK_TIMEOUT nor NETWORK_ERROR carries a structured
// code to interrogate at all; the request never got far enough to
// interrogate one from. Retrying cold after a timeout can only cost the
// SAME budget again, doing STRICTLY MORE work (a full cold download
// instead of a cache-accelerated one) — so whenever the original timeout
// was NOT actually caused by the cache (the build itself is simply
// large/slow, e.g. §19.4's own "may provision local service stacks, run
// codegen, seed local state"), the cold retry is slower still and, having
// the identical budget, times out again — doubling wall clock and
// converting one slow-but-viable attempt into a guaranteed failure. A bare
// timeout is not evidence of cache trouble; it is evidence only that SOME
// request took too long.
//
// NETWORK_ERROR (every other transport-level failure — connection
// refused, DNS failure, ...) is excluded for the same underlying reason,
// generalized: this adapter's own invented protocol is a single POST
// endpoint (/v1/images) carrying both the build and the cache-mount
// request together, so a failure to connect AT ALL says something about
// reaching the whole build service, never something specific to the
// cache — there is no scenario in this protocol where the connection
// itself fails BECAUSE of the cache mount specifically but would have
// succeeded without one. Retrying cold when the endpoint is unreachable
// cannot plausibly change that outcome; it only adds a wasted round trip
// (never a wall-clock-doubling failure the way the timeout case does, but
// motivated by the identical principle stated below).
//
// The general principle: a signal only belongs in this function if it is
// SPECIFIC to the cache mount on THIS request — a genuine reply the build
// service sent back after processing (however imperfectly formed), never
// the mere ABSENCE of a reply. The genuine "the cache subsystem itself
// hung" case a client-side timeout used to (mis)represent is now
// represented honestly instead: a well-behaved build service reports its
// OWN internal cache-mount timeout via the structured CACHE_MOUNT_TIMEOUT
// code (signal 1 above), fast, before the client's own budget is anywhere
// near exhausted — only THAT structured self-report is trusted, never an
// inference from this client's own timeout or connection failure.
//
// A nil err, or one that is not a *ports.ProviderError at all (whether
// directly or wrapped), is never cache trouble.
func isCacheMountTrouble(err error) bool {
	var pe *ports.ProviderError
	if !errors.As(err, &pe) {
		return false
	}
	if cacheMountTroubleCodes[pe.Code] {
		return true
	}
	return pe.Transient && strings.HasPrefix(pe.Code, httpCodePrefix)
}
