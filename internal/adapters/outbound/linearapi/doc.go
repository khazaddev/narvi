// Package linearapi holds direct, narrow calls against Linear's real
// public API (https://api.linear.app) -- Linear's own OAuth2 token
// endpoint (oauth.go) and its GraphQL API (graphql.go), both verified
// live against Linear's current developer documentation during Step 34's
// ("Linear ingress", §8.10) own investigation.
//
// Scope note (Step 34): this package deliberately does NOT implement the
// general Notifier port / outbox-consumer abstraction Step 35 owns
// (§5.1's "Outbox pattern for every outbound side effect... a retry
// worker delivers with exponential backoff + dead-letter") -- there is no
// ports.Notifier interface yet (internal/app/ports has none), and the
// outbox table has no real delivery-worker consumer yet either. What this
// package DOES provide is the smallest immediate outbound need Linear's
// own AgentSession/AgentActivity model imposes on ingress itself: Linear
// marks a brand-new agent session "unresponsive" unless it sees an
// activity within 10 seconds of the `created` webhook (confirmed against
// Linear's real docs), so internal/adapters/inbound/linear's own webhook
// handler calls CreateThoughtActivity below SYNCHRONOUSLY, directly, right
// after creating the backing Narvi session -- not queued through an
// outbox a worker might not drain for seconds or minutes. A future Step
// that builds the real Notifier/outbox consumer can layer richer,
// retried, asynchronous AgentActivity updates (the "progressive" half of
// §8.10) on top of this same Client without this Step's own minimal call
// needing to change.
package linearapi
