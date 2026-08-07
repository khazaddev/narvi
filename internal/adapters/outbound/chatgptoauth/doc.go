// Package chatgptoauth is the control plane's own small, direct outbound
// HTTP client for the ChatGPT-account (Codex) OAuth device-authorization
// flow (Step 59, §29.2/§29.3/§29.9) -- the "one small CP-side outbound
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
// Every ENDPOINT PATH and every FIELD NAME below is sourced to §29.2,
// which was itself extracted from the pinned OpenCode 1.17.15 binary's own
// embedded implementation of this flow (read directly, not guessed) and
// cross-checked against OpenAI's published Codex CLI auth docs. What §29.2
// does NOT state explicitly, and this environment has no real OpenAI
// credential to verify live (this Step's own hard constraint: "no real
// ChatGPT/OpenAI/Gemini credentials exist in this environment"), is the
// exact request/response CONTENT TYPE for two of the four calls:
//
//   - usercode/token (the two custom `/api/accounts/deviceauth/*`
//     endpoints): implemented here as JSON request/response, matching
//     their own "/api/..." path convention (this codebase's own other
//     JSON APIs all live under an "/api/..." prefix too) and matching
//     every field name §29.2 gives being a single flat object with no
//     indication of form-encoding.
//   - the standard `/oauth/token` endpoint (both grant types): implemented
//     here as RFC 6749 §4's own standard `application/x-www-form-
//     urlencoded` body -- the canonical, near-universal encoding for an
//     OAuth2 token endpoint, and §29.2 itself frames this exact path as
//     "Codex CLI's own public client" hitting the ordinary OAuth token
//     endpoint, not a bespoke OpenAI API.
//
// This is the single largest unverified-shape risk in this Step's own
// implementation, named here exactly as the hard constraint requires
// ("if you need a shape §29 does not give you ... stop and report the
// gap") -- every endpoint PATH and FIELD NAME is real and verified;
// only the wire ENCODING of the two custom endpoints is this package's
// own reasoned inference, not a live-verified fact. See this Step's own
// landing PR description for the same accounting.
package chatgptoauth
