// Package wshub holds the inbound WS hub for both the sandbox socket and
// the client socket (contract in §6).
//
// # What's real now: the sandbox socket (Step 18)
//
// NewSandboxHandler (sandbox.go) backs GET /sessions/{sessionID}/ws
// ?type=sandbox (§6.1) -- the server-side mirror of
// internal/sandboxagent/wsbridge's client side. It implements the full
// 10-step handshake status-code table (see sandbox.go's own doc comment),
// then hands the live connection to readLoop (dispatch.go), which peeks
// each inbound frame's common envelope fields (type/gen/lastBootPhase/
// ackId), delivers a sessionactor.SandboxEvent to the session's own Actor
// (internal/app/sessionactor), and writes back a sandboxws.Ack when the
// Actor's reply carries a non-empty AckID. token.go implements the sandbox-
// bearer-token verify side (HashSandboxToken + verifySandboxToken) against
// sandboxes.token_hash (migrations/000015_sandbox_token_hash.up.sql).
//
// This Step's own outbound capability is ack-only (the plan row's own "ack
// receipt" wording): it does not send prompt/stop/push/snapshot/shutdown/
// git_sync_complete to a live connection, and builds no live-connection
// registry/broadcast mechanism for that -- Step 21 ("e2e happy path") owns
// actually dispatching outbound commands.
//
// # What's still a stub: the client socket (Step 19)
//
// No client-hub route, handler, or dispatch exists yet -- NewSandboxHandler
// itself 400s any request whose `type` query param isn't exactly
// "sandbox", which is precisely the reservation Step 19 needs.
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
package wshub
