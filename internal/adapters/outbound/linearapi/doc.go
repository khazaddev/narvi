// Package linearapi holds direct, narrow calls against Linear's real
// public API (https://api.linear.app) -- Linear's own OAuth2 token
// endpoint (oauth.go) and its GraphQL API (graphql.go), both verified
// live against Linear's current developer documentation during §8.10's
// ("Linear ingress", §8.10) own investigation.
//
// Scope note (§8.10): this package deliberately does NOT implement the
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
//
// Audit finding M16 ("completeness"): that future Step is this one. Step
// 35 shipped the real Notifier/outbox consumer, but nothing ever enqueued
// a mid-turn update through it -- a Linear-origin session's own agent
// session got exactly one outbox notification ever (the terminal one, at
// turn completion), no matter how long the turn ran. An audit-fix batch
// now layers exactly the "progressive" update this comment anticipated on
// top of THIS SAME Client, unchanged: internal/app/outboxworker's own
// linearNotifier calls CreateThoughtActivity below (the same call, now
// ALSO reachable asynchronously, via ports.NotificationKindLinearProgress)
// once a Linear-origin session's turn processes its first tool_call wire
// event -- see internal/app/sessionactor/progressnotify.go's own doc
// comment for the full design.
package linearapi
