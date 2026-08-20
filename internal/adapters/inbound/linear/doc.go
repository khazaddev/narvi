// Package linear implements Linear ingress ("Linear ingress",
// §8.10) end to end:
//
//   - Workspace OAuth connection (install.go, callback.go, oauth.go): a
//     Linear WORKSPACE admin authorizes Narvi's own Linear app at
//     workspace scope (Linear's real OAuth2 flow, with its own
//     `actor=app` authorization-url parameter -- confirmed against
//     Linear's real, current developer docs during this Step's
//     investigation), so the control plane can later call Linear's own
//     API on that workspace's behalf. This is DELIBERATELY NOT a second
//     way for a human to sign into Narvi itself -- that is Step 20's
//     already-shipped GitHub OAuth login (internal/adapters/inbound/auth),
//     entirely untouched by this package.
//   - Webhook ingress (webhook.go, signature.go, payload.go): Linear's
//     real AgentSessionEvent webhook, verified with Linear's own real
//     signature scheme (Linear-Signature header, hex HMAC-SHA256 of the
//     raw request body -- see signature.go's own doc comment for the full
//     investigation), deduped via WebhookDeliveryStore.Claim (provider
//     "linear", delivery id = the Linear-Delivery header), and routed to
//     a Narvi session/turn via LinearAgentSessionStore's own atomic claim
//     on Linear's own AgentSession id (see migrations/
//     000030_linear_agent_sessions.up.sql's own doc comment for why that
//     id is already a 1:1-with-one-unit-of-work identity, unlike
//     GitHub's/Slack's own coalescing problems).
//
// Progressive AgentActivity updates (the OUTBOUND half of §8.10) are
// deliberately minimal in this Step: only the single acknowledgment
// activity Linear's own docs require within 10 seconds of a `created`
// event (internal/adapters/outbound/linearapi.CreateThoughtActivity,
// called directly and synchronously) -- the general, retried,
// asynchronous Notifier/outbox-consumer path is Step 35's job.
package linear
