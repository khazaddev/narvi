// Package chatgptlink implements the ChatGPT-account (Codex) OAuth link
// flow's own control-plane orchestration (§29.3): StartLink
// begins (or reuses a still-live) device-flow attempt, PollLink drives it
// forward at most one upstream step per call, and Unlink removes a linked
// account. There is deliberately NO background worker here — §29.3 point
// 2 is explicit that "the human sitting on the [Settings] page IS the
// polling loop": every advance happens synchronously inside a GET
// /api/me/chatgpt-link request, gated by chatgpt_link_attempts.
// last_polled_at against the server-provided interval, so nothing leaks
// when the page is simply abandoned (the attempt row just expires).
//
// This is a genuinely different kind of "link" than internal/app/
// identitylink's own auto-linking (§13.2): that package matches an
// EXTERNAL ingress identity (a Slack/Linear/GitHub account) to a Narvi
// user by verified email, unattended, from inbound events. This package
// is always a direct, self-service action BY an already-authenticated
// Narvi user, linking a personal, subscription-tied OAuth credential —
// never an identities row (§29.3's own "deliberately NOT an identities
// row" ruling) — stored instead as a provider_credentials scope=user/
// kind=oauth row (internal/domain/providercredential, §29.4).
//
// The refresh token this flow obtains is written to Postgres (encrypted)
// exactly once, here, and never read back by this package again — the
// refresh pump (internal/app/chatgptrefresh, §29.5) is the only other
// code that ever touches it, and the sandbox-facing delivery endpoint
// (internal/adapters/inbound/httpapi/providercredentialsdelivery.go)
// structurally cannot forward it to a sandbox (§29.6).
package chatgptlink
