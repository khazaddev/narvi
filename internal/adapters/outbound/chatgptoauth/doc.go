// Package chatgptoauth is the control plane's own small, direct outbound
// HTTP client for the ChatGPT-account (Codex) OAuth device-authorization
// flow (§29.2/§29.3/§29.9) -- the "one small CP-side outbound
// adapter (four HTTP calls: usercode, token-poll, code exchange, refresh
// -- §29.2's shapes)" §29.9 specifies, structurally mirroring internal/
// adapters/outbound/githubapi's own "plain *http.Client wrapper,
// OAuth-bearer-token-authenticated per call" shape (the closest existing
// precedent identified for this Step: also a real third-party HTTP API
// called directly, never proxied through a sandbox).
//
// This package deliberately does NOT touch OpenCode's own `/provider/
// {providerID}/oauth/*` endpoints -- §29.9 considered and rejected
// brokering the flow through a spawned OpenCode process specifically
// because the control-plane image does not ship the OpenCode binary
// (brokering would mean spawning a sandbox per Settings click, §3.2's
// heaviest machinery on an interactive path) and because OpenCode's own
// API deliberately never exposes a way to read back a stored token (no
// GET /auth, §29.1) -- the harvest would be a file read behind the API's
// own back. This package instead speaks auth.openai.com directly.
//
// # Verified vs. inferred
//
// Every ENDPOINT PATH and FIELD NAME below is sourced to §29.2, itself
// extracted from the pinned OpenCode 1.17.15 binary's own embedded
// implementation of this flow (read directly, not guessed) and cross-
// checked against OpenAI's published Codex CLI auth docs.
//
// This environment has no real, authenticated OpenAI credential (this
// Step's own hard constraint), so the actual code-exchange/refresh grant
// bodies were never exercised live. But §29.2's own device-flow START is
// deliberately UNAUTHENTICATED (a client only needs its own public
// client_id) -- so this package's own usercode_canary_test.go (§29.7's
// named scheduled canary; gated behind the "canary" build tag, never a
// PR gate) made ONE real, live call against auth.openai.com during this
// Step's own implementation, and found §29.2's own field list was
// INCOMPLETE for usercode's response:
//
//   - Content type CONFIRMED JSON both ways for the two custom
//     `/api/accounts/deviceauth/*` endpoints -- the original inference
//     (matching their own "/api/..." path convention) was correct.
//   - interval is a STRING on the wire ("5"), not a JSON number as §29.2's
//     own prose implied -- parsed to time.Duration by StartDeviceAuth.
//   - The response ALSO carries a fourth field, expires_at (RFC 3339),
//     never named by §29.2 at all -- this device code's own real,
//     server-provided expiry, used directly by internal/app/chatgptlink
//     rather than any Narvi-side invented TTL.
//   - deviceauth/token's own "pending" response was independently
//     confirmed too: a real, unapproved (device_auth_id, user_code) pair
//     returns HTTP 403 with a real OAuth-style error body ({"error":
//     {"code": "deviceauth_authorization_pending", ...}}) -- matching
//     §29.2's own "403/404 = pending" finding exactly.
//
// Genuinely still UNVERIFIED live (no real human device-approval or valid
// authorization_code/refresh_token was available to exercise these):
// deviceauth/token's own GRANTED response shape ({authorization_code,
// code_verifier}), and the standard /oauth/token endpoint's exact
// request encoding for both grant types -- implemented here as RFC 6749
// §4's own standard application/x-www-form-urlencoded body, the
// canonical encoding for an OAuth2 token endpoint, and §29.2 itself
// frames this exact path as hitting the ordinary OAuth token endpoint,
// not a bespoke OpenAI API, but this remains this package's own reasoned
// inference, not a live-verified fact. Named here exactly as the hard
// constraint requires ("if you need a shape §29 does not give you ...
// stop and report the gap") -- see this Step's own landing PR description
// for the same accounting.
package chatgptoauth
