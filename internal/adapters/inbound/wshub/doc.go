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
// Outbound dispatch (Step 21, "e2e happy path"): commander.go's own
// SandboxRegistry is the single-recipient (not fan-out) live-connection
// registry that implements internal/app/ports.SandboxCommander --
// NewSandboxHandler registers each connection with it right before
// entering readLoop, and internal/app/sessionactor's own EnsureDispatched
// handling calls SandboxCommander.SendCommand to push a real sandboxws.
// Prompt onto a session's live connection. A concurrent SendCommand write
// and dispatch.go's own writeAck (running on the connection's own
// read-loop goroutine) are documented-safe per coder/websocket.Conn's own
// docs ("All methods may be called concurrently except for Reader and
// Read") -- verified directly via `go doc github.com/coder/websocket.Conn`
// during this Step's own design phase, not merely cited.
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
//     at spawn time) now exists (Step 21's own tryPlanSpawn,
//     internal/app/sessionactor/dispatch.go, calling platform.
//     GenerateToken + UpsertSandboxForSpawn) -- HashSandboxToken was
//     exported specifically so that caller could reuse this package's own
//     hashing convention rather than reinventing it, and it does.
//     verifySandboxToken's own nil-token_hash bypass (any non-empty
//     bearer token accepted while token_hash is NULL) therefore no longer
//     "covers every row today" -- every row this Step's own real spawn
//     path creates always sets token_hash. The bypass remains, unchanged,
//     as defense-in-depth for a legacy or manually-inserted row that
//     genuinely has no hash set, not because it is the normal path any
//     real spawn takes. HashSandboxToken is also this Step's own second
//     caller, internal/adapters/inbound/httpapi/scmcredentials.go, which
//     deliberately does NOT copy this bypass -- see that file's own
//     verifySandboxBearerToken doc comment for why (it hands back a real,
//     live OAuth credential on success, a materially higher-stakes
//     endpoint than gating a WS connection).
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
