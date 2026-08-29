// Package shadowslack implements §30.3's second compensating control for
// Slack: "one client per provider ... mutation methods behind decorated
// interfaces, and ingress packages losing the ability to construct their
// own clients."
//
// # Why this is a client-method decorator, not a transport gate
//
// §30.2's GitHub layer 0 (internal/adapters/outbound/githubapi's
// shadowRoundTripper) works because a mutating GitHub request always names
// the repository it targets in its own URL path (/repos/{owner}/{repo}/...),
// so the gate can resolve the per-repo egress flag from the request alone.
// Slack's Web API carries no such thing: chat.postMessage/chat.postEphemeral/
// chat.update/views.open are all POSTs to fixed, repo-less endpoints, and
// (unlike GitHub) even Slack's OWN read/write split does not line up with
// HTTP verbs cleanly enough to key a transport gate on. There is nothing in
// the wire request a RoundTripper could resolve a repository from. So this
// package decorates at the CLIENT-METHOD level instead, exactly like Linear
// must (internal/app/shadowlinear's own doc comment) -- the decision is made
// before the request is ever built, using context the CALLER already has.
//
// # Why one fixed repository, not a per-call one
//
// Every session this codebase's Slack ingress can ever create is created
// against exactly one, deployment-wide-configured repository
// (platform.Config.SlackDefaultRepoName/SlackDefaultRepoURL --
// internal/adapters/inbound/slack/handler.go's own resolveOrClaimSession,
// the ONLY place a Slack-originated session is ever created, always passes
// exactly that one repo). There is no per-workspace/per-channel repo
// mapping in this codebase's Slack integration. So the repository this
// package's Decorator checks §30.8's live_egress_enabled flag against is
// resolved ONCE, at process-wiring time (cmd/control-plane/main.go), not
// per call or per session -- there is only ever one answer to "which
// repository does this Slack integration act on" for this deployment, and
// resolving it per call would only add a database round trip to every ack
// for a value that cannot change without a restart. What DOES vary per
// call, and is therefore re-resolved on every single mutation (never
// cached), is whether that one fixed repository is CURRENTLY live or
// shadow -- the same "resolved per call, never cached" discipline
// shadowscm.Decorator and githubapi.NewGatedClient already establish, for
// the identical reason: a cached answer keeps suppressing after a
// promotion and keeps emitting after a demotion.
//
// # What is decorated, and what is not
//
// Reads (GetUserEmail, Slack's own users.info) are forwarded to the live
// client unchanged: observing a Slack user's profile leaves no trace in
// the customer's workspace, and suppressing it would make identity
// auto-linking impossible rather than safe, mirroring shadowscm.Decorator's
// identical treatment of ports.SourceControl's own reads. Every other
// method on Client is a write into the customer's Slack workspace and is
// suppressed-and-recorded exactly like shadowscm.Decorator's port writes,
// using the same shadowledger.Store this codebase's other suppressed
// effects (SCM writes, the GitHub transport gate, the shadow credential
// mint) already write into -- see shadowledger.SlackAck/SlackEphemeral/
// SlackMessageUpdate/SlackViewOpen for the token-free record types.
package shadowslack
