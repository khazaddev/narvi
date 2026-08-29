// Package shadowlinear implements §30.3's second compensating control for
// Linear: "one client per provider ... mutation methods behind decorated
// interfaces."
//
// # Why this is a client-method decorator, and never the transport
//
// §30.3 names this explicitly: a verb-level transport guard, the shape
// §30.2's GitHub layer 0 uses, does NOT work for Linear, because Linear's
// entire API is one endpoint -- every call, read or write, is
// POST /graphql. A RoundTripper cannot distinguish CreateThoughtActivity
// from GetUserEmail by method or path; both are an identical POST to an
// identical URL with only the marshalled body differing. Suppression must
// therefore happen before the request is ever built, at the client-method
// level, using context the caller already has -- exactly like
// internal/app/shadowslack, and for the same reason stated there in more
// detail (that package's doc comment).
//
// # Why one fixed repository, not a per-call one
//
// Every session this codebase's Linear ingress can ever create is created
// against exactly one, deployment-wide-configured repository
// (platform.Config.LinearDefaultRepoName/LinearDefaultRepoURL --
// internal/adapters/inbound/linear/webhook.go's own session-creation path
// always passes exactly that one repo, mirroring Slack's identical
// single-default-repo shape). The repository this package's Decorator
// checks §30.8's live_egress_enabled flag against is therefore resolved
// once, at process-wiring time (cmd/control-plane/main.go) -- see
// shadowslack's own doc comment for the fuller reasoning, which applies
// here verbatim. What varies per call, and is re-resolved on every single
// mutation, never cached, is whether that one fixed repository is
// currently live or shadow.
//
// # What is decorated, and what is not
//
// GetUserEmail is a read, forwarded to the live client unchanged, mirroring
// shadowscm.Decorator's and shadowslack.Decorator's identical treatment of
// their own reads. CreateThoughtActivity and CreateResponseActivity are
// writes into the customer's Linear workspace and are suppressed-and-
// recorded using this codebase's one shared shadowledger.Store -- see
// shadowledger.LinearThoughtActivity/LinearResponseActivity for the
// token-free record types (accessToken, Linear's own per-workspace
// installation token, never enters the ledger, exactly like every other
// spec type in that package).
//
// CreateResponseActivity has a SECOND, separate production call site this
// package does not touch: the outbox-delivered turn-outcome notification
// (internal/app/outboxworker's own LinearNotifier), already covered by
// §30.2's outbox classification (ports.NotificationKindLinear is
// ClassSuppress there). This package decorates only the SYNCHRONOUS calls
// internal/adapters/inbound/linear makes directly from its own webhook
// handler -- §30.3's family 4.
package shadowlinear
