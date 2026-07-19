// Package githubapi holds the GitHub SourceControl port implementation
// (createPR, credential minting, push specs) — implemented at Step 21
// ("e2e happy path", §4.3). Credential minting and push specs are handled
// elsewhere (internal/adapters/inbound/httpapi's own scm-credentials
// endpoint mints the git-over-https credential; cmd/sandbox-agent's own
// HandlePush builds the push spec) -- this package's own real job is
// exactly ports.SourceControl.CreatePR: a real POST https://api.github.com/
// repos/{owner}/{repo}/pulls call.
//
// Adapter accepts an apiBaseURL constructor parameter (defaulting to
// "https://api.github.com" in production wiring, cmd/control-plane/main.go)
// so tests can override it with a local httptest.Server standing in for
// GitHub's real API -- the SAME apiBaseURL-parameter precedent
// internal/adapters/inbound/auth's own NewCallbackHandler already
// established for testability.
//
// Auth is a GitHub OAuth user access token (Authorization: Bearer
// <token>), the SAME token design decision 8's scm-credentials endpoint
// already decrypted for this session's own user -- a separate, correct use
// of that token from the git-over-https "x-access-token"/password
// convention used for the PUSH itself (see cmd/sandbox-agent's own
// HandlePush): these are two distinct, correct conventions built on the
// SAME underlying OAuth token, never conflated here.
package githubapi
