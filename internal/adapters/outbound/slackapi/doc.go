// Package slackapi holds the Slack Notifier adapter ("outbox
// delivery", §5.1/§8.10) -- a real chat.postMessage client implementing
// ports.Notifier, consumed EXCLUSIVELY via the outbox (internal/app/
// outboxworker), never called directly by any inbound handler.
//
// This is a DELIBERATE, small duplication of internal/adapters/inbound/
// slack/ack.go's own tiny chat.postMessage HTTP shape (request/response
// envelope, bounded-read, Authorization: Bearer botToken auth), not an
// oversight to "fix" by sharing one client across both packages: that
// file's own doc comment already says so explicitly ("NOT the general
// Notifier/outbox abstraction Step 35 builds"), and ack.go's own call is a
// synchronous, in-request-path, best-effort in-thread ack (Step 33's own
// scope, called directly from the inbound webhook handler, no retry, no
// outbox row) -- a fundamentally different caller shape from THIS
// package's Client, which is called ONLY by the outbox delivery worker,
// asynchronously, with its own retry/backoff/dead-letter policy layered on
// top by that caller, never by any inbound handler directly. Two small,
// independent clients, each scoped to its own caller, is simpler and
// safer than one shared client serving two structurally different
// call sites with different lifecycle/retry expectations.
package slackapi
