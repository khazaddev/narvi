// Package wshub holds the inbound WS hub for both the sandbox socket and
// the client socket (contract in §6), both mounted at the SAME route
// (GET /sessions/{sessionID}/ws), discriminated by ?type=sandbox vs
// ?type=client via handler.go's own NewHandler dispatcher.
//
// # The sandbox socket (Step 18)
//
// NewSandboxHandler (sandbox.go) backs ?type=sandbox (§6.1) -- the
// server-side mirror of internal/sandboxagent/wsbridge's client side. It
// implements the full 11-step handshake status-code table (see
// sandbox.go's own doc comment), then hands the live connection to
// readLoop (dispatch.go), which peeks each inbound frame's common
// envelope fields (type/gen/lastBootPhase/ackId), delivers a
// sessionactor.SandboxEvent to the session's own Actor
// (internal/app/sessionactor), and writes back a sandboxws.Ack when the
// Actor's reply carries a non-empty AckID. token.go implements the sandbox-
// bearer-token verify side (HashSandboxToken + verifySandboxToken) against
// sandboxes.token_hash (migrations/000015_sandbox_token_hash.up.sql).
//
// This Step's own outbound capability is ack-only (the plan row's own "ack
// receipt" wording): it does not send prompt/stop/push/snapshot/shutdown/
// git_sync_complete to a live connection -- Step 21 ("e2e happy path")
// owns actually dispatching outbound sandbox commands.
//
// # The client socket (Step 19)
//
// NewClientHandler (client.go) backs ?type=client (§6.2) -- see that
// file's own top comment for the complete handshake outcome table
// (sessionID/session-row validation -> Accept (deliberately WITHOUT
// sandbox.go's InsecureSkipVerify -- this socket is browser-facing, same-
// origin enforcement matters here, see client.go's own contrast comment)
// -> bounded subscribe-frame read -> ws-token verify (close 4001 re-auth /
// 4002 expired) -> a single `subscribed` reply (SubscribedPayload) ->
// registration with the shared *Hub for live broadcast -> a read loop
// handling `fetch_history` cursor-paginated replay). *Hub (also in
// client.go) is the in-process, session-keyed connection registry that
// implements internal/app/ports.EventBroadcaster -- the session actor's
// own successful transact commits call Hub.Broadcast, which fans a raw
// stored event payload out to every subscribed connection for that
// session, sent exactly as stored (§6.2: no wrapper envelope for a live
// broadcast frame).
//
// # Honest gaps this package documents rather than papers over
//
//   - X-Sandbox-ID is checked for PRESENCE only, never verified against a
//     real value -- no Step yet wires a real provider-assigned
//     sandbox-instance id into the sandbox's own environment (matching
//     internal/sandboxagent/wsbridge/doc.go's own identical gap on the
//     client side, and Step 13's NARVI_IMAGE_DIGEST gap).
//   - ErrSessionActorElsewhere (this process doesn't hold the session's
//     advisory lock) maps to 503, not one of wsbridge's own 4 "fatal"
//     statuses (401/403/404/410) -- deliberately, so the sandbox-agent's
//     own already-built exponential-backoff reconnect loop is what
//     recovers, since no cross-pod routing/proxy exists anywhere in the
//     codebase yet. This is a known, honest, currently-unaddressed
//     limitation, not a permanent solution -- not scoped to any specific
//     later Step by the plan.
//   - Sandbox token MINTING (generating a fresh token + writing its hash
//     at spawn time) does not exist yet -- HashSandboxToken is exported
//     specifically so the future minting Step (§5.2, Step 21+, once a
//     real SandboxProvider.Spawn call exists) can call it directly rather
//     than reinventing its own hashing convention; verifySandboxToken
//     accepts any non-empty bearer token while token_hash is NULL (every
//     row today), an explicit, forward-compatible bridge.
//   - Suspect-state recovery-during-grace ("any liveness signal during
//     grace returns to previous state", §3.2) is Step 24's own job
//     ("two-phase terminalization") -- a Suspect sandbox reconnecting
//     through this package IS allowed (IsDeadSandboxStatus(Suspect) is
//     false) and its events DO get persisted/bump liveness
//     (internal/app/sessionactor's own handleSandboxEvent), but no
//     recovery transition fires for it.
//   - Real user auth/identity now exists (Step 20, "auth v1") -- REST
//     ws-token minting (internal/adapters/inbound/httpapi's own
//     MintWSToken) is gated behind internal/adapters/inbound/auth.
//     Middleware and scopes ws_tokens.user_id to the real authenticated
//     caller. This package's own client-WS subscribe-time verification
//     (client.go) is UNCHANGED by that Step: it still only checks the
//     presented ws-token's hash against ws_tokens, never anything
//     per-participant beyond that. participants stays completely
//     untouched (SubscribedPayload.participants is always an empty
//     array) -- real user identity existing now is not the same as
//     multiplayer/presence being wired, which is its own, distinct,
//     not-yet-scoped concern (§8.11) -- see
//     internal/adapters/inbound/httpapi/doc.go and
//     internal/adapters/inbound/auth/doc.go for the full writeup.
//   - Cross-pod broadcast fan-out is NOT solved here: *Hub only ever
//     reaches connections registered in the SAME process as the actor
//     that persisted the event -- the same class of honest gap as
//     ErrSessionActorElsewhere/503 above, not a Postgres LISTEN/NOTIFY or
//     message-bus solution, genuinely out of scope for this Step.
package wshub
