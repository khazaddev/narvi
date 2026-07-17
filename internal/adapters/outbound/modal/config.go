package modal

import "github.com/khazaddev/narvi/internal/platform"

// Config configures a Modal Provider (New). Every field is sourced from
// the caller's own configuration/params — New never hardcodes an API
// base URL, token, or proxy.
type Config struct {
	// BaseURL is Modal's API base URL (e.g. "https://api.modal.com" in
	// production; an httptest.Server's URL in tests). Required; must be
	// an absolute URL (scheme + host) — New returns InvalidBaseURLError
	// otherwise.
	BaseURL string

	// AuthToken is the bearer token attached to every Modal API request
	// (Authorization: Bearer <AuthToken>). Required — New returns
	// MissingConfigError otherwise.
	AuthToken string

	// Timeouts supplies platform.Timeouts.ProviderHTTPClientTimeout,
	// used as the constructed *http.Client's Timeout (§4.1: "The
	// provider HTTP client timeout MUST exceed the provider's worst
	// cold-start"), and platform.Timeouts.ProviderWorstColdStart, which
	// New checks it against as a defense-in-depth assertion — even
	// though platform.Timeouts.Validate() already enforces this
	// pairwise invariant at control-plane boot for the shipped defaults,
	// New re-checks it here in case it is ever called with a
	// caller-constructed Timeouts value that skipped that validation.
	Timeouts platform.Timeouts

	// EgressProxyURL optionally routes every Modal request through a
	// configurable egress proxy (§4.1: "All Modal traffic goes through
	// the configurable egress proxy"). Empty (the default) means a
	// direct connection: the egress proxy is optional and fail-open at
	// this layer. A non-empty but malformed value is a fail-fast error
	// from New (InvalidEgressProxyURLError) — never a silently-ignored
	// config value.
	EgressProxyURL string
}
