// Package httpapi holds the REST adapter serving the web UI and external
// clients (§6.3): "The BFF-facing routes: sessions CRUD/create, events,
// artifacts, secrets, environments, automations, uploads, ws-token."
// Step 19's own plan row explicitly narrows that to exactly
// "create/get/events/artifacts" (+ ws-token, named separately by §6.2) --
// secrets/environments/automations/uploads are NOT this Step's job (see
// contracts/README.md's own scope note on rest/v1/dtos.schema.json).
//
// # Routes (all under /api/sessions, mounted by cmd/control-plane/main.go,
// behind internal/adapters/inbound/auth.Middleware as of Step 20 -- see
// that package's own doc.go)
//
//   - POST /api/sessions -- create.go's CreateSession: decodes
//     restdtos.CreateSessionRequest (body capped via http.MaxBytesReader,
//     see create.go's own doc comment), validates repos is non-empty,
//     inserts via SessionStore.Create, responds 201 with restdtos.Session.
//   - GET /api/sessions/{sessionID} -- get.go's GetSession: 404 if not
//     found, else 200 with restdtos.Session.
//   - GET /api/sessions/{sessionID}/events?cursor=&limit= -- events.go's
//     ListEvents: 404 if the session doesn't exist, else 200 with
//     restdtos.EventsResponse (shaped exactly like client-ws/v1's own
//     FetchHistoryResponse -- the same underlying EventStore.
//     ListForSession read the client WS hub's own fetch_history uses, one
//     implementation, two callers).
//   - GET /api/sessions/{sessionID}/artifacts -- artifacts.go's
//     ListArtifacts: 404 if the session doesn't exist, else 200 with
//     restdtos.ArtifactsResponse.
//   - POST /api/sessions/{sessionID}/ws-token -- wstoken.go's MintWSToken:
//     404 if the session doesn't exist, else mints a fresh token
//     (platform.GenerateToken), stores only its hash
//     (platform.HashToken), and responds 200 with
//     restdtos.WSTokenResponse{Token: <plaintext>, ExpiresAt}.
//
// # The auth gap -- resolved (Step 20, "auth v1")
//
// Every route above is now mounted behind internal/adapters/inbound/auth.
// Middleware in cmd/control-plane/main.go: a request with no valid
// narvi_auth_session cookie gets 401 before reaching any handler in this
// package. Both of Step 19's own honest gaps are directly closed as a
// result:
//
//   - CreateSession now inserts created_by with the REAL authenticated
//     caller's id (platform.UserFromContext, resolved via
//     authenticatedUserID in helpers.go) instead of always NULL.
//   - MintWSToken now scopes ws_tokens.user_id to the REAL authenticated
//     caller the same way, instead of always NULL.
//
// # Step 21 ("e2e happy path") updates
//
// CreateSession now actually PERSISTS req.Repos (design decision 1,
// migrations/000018_session_repos.up.sql) and, when req.Prompt is
// non-nil, inserts a Turn row in the SAME transaction as the session
// insert, then fires EnsureDispatched on that session's own actor (see
// create.go's own doc comment for the full sequencing) -- its own
// constructor signature grew a *pgxpool.Pool, a *postgres.TurnStore, and
// a *sessionactor.Registry parameter as a result.
//
// A SIXTH route is added, mounted OUTSIDE this package's own /api/sessions
// group and outside auth.Middleware entirely (see scmcredentials.go's own
// doc comment for why):
//
//   - POST /sessions/{sessionID}/scm-credentials -- scmcredentials.go's
//     own ScmCredentials: a sandbox-bearer-token-authenticated endpoint
//     (mirrors wshub's own header-bearer-token handshake precedent, NOT
//     Step 20's cookie-based auth.Middleware) that decrypts and hands
//     back the session's own created_by user's GitHub OAuth access token
//     as a git-over-https credential (§5.2). This is the control-plane
//     side of the wire contract internal/sandboxagent/credentials.
//     CPClient (Step 15) already built and tested the client side of.
//
// # Step 22 ("snapshots & restore") updates
//
// A SEVENTH route is added, mounted the SAME way scm-credentials is
// (outside /api/sessions and outside auth.Middleware entirely):
//
//   - POST /sessions/{sessionID}/snapshot -- snapshotmint.go's own
//     SnapshotMint: another sandbox-bearer-token-authenticated endpoint,
//     called by cmd/sandbox-agent's real HandleSnapshot (via
//     internal/sandboxagent/snapshotclient) once the control plane sends
//     a "snapshot" WS command (design decision 1). Calls the real
//     ports.SandboxProvider.TakeSnapshot for the session's live sandbox
//     and hands back the real resulting snapshot id, which the sandbox
//     then reports back over the WS bridge as a CRITICAL "snapshot_ready"
//     event (design decision 2's own full round-trip reasoning).
//
// # Step 28 ("turn recovery") update
//
// An EIGHTH route is added, INSIDE the /api/sessions group (so it shares
// that group's own auth.Middleware gate, unlike scm-credentials/snapshot
// above):
//
//   - POST /api/sessions/{sessionID}/turns -- turn.go's own CreateTurn:
//     §8.7's "relaunch-and-resume" REST API. Enqueues a new Pending turn
//     on an EXISTING session (404 if it doesn't exist; 409 if a turn is
//     already in flight for it), then fires the SAME GetOrSpawn +
//     Send(EnsureDispatched{}) sequencing CreateSession already uses for
//     its own optional first turn. No "resume" flag exists anywhere on
//     this route: sessions.opencode_conversation_id already persists
//     across turns, and dispatch.go's own buildPromptPayload already
//     carries it on every Prompt automatically -- so the new turn
//     transparently continues whatever OpenCode conversation the session
//     already has, the instant it dispatches.
//
// Wiring participants/presence (§8.11) is still untouched -- a distinct,
// not-yet-scoped concern.
//
// # Step 31 ("webhook toolkit") update
//
// No new route is added by this Step -- it adds no concrete provider
// wiring at all. CreateSession's own doc comment above (create.go) is
// split in two: everything after decoding the request body is now
// createSessionCore (unexported as of this Step -- Step 33 exports it,
// see below), taking an already-decoded restdtos.CreateSessionRequest
// plus a NULLABLE creator (pgtype.UUID, Valid == false for "no human
// caller"). CreateSession itself is now a thin wrapper: decode body ->
// authenticatedUserID (UNCHANGED, still hard-required for this browser/
// REST route) -> createSessionCore -> write the JSON response. This is a
// pure refactor for this route -- every existing test in this package's
// own _test.go files passes unchanged. The point of the split is reuse:
// Steps 32/33/34's own GitHub/Slack/Linear webhook ingress handlers call
// createSessionCore directly, with their own already-verified,
// already-decoded request and a NULL creator (no cookie, no human) --
// never this package's own CreateSession, which stays browser-only.
// Whether those ingress handlers end up living in THIS package (as new
// files alongside create.go/get.go/events.go/artifacts.go/wstoken.go) or
// export createSessionCore to reach it from their own separate packages
// is left open by this Step -- see the Step 33 update immediately below
// for how that question was actually resolved.
//
// # Step 33 ("Slack ingress") update
//
// internal/adapters/inbound/slack (a separate package, mirroring
// github/linear's own reserved package-per-surface shape) is Step 33's
// own new Slack webhook ingress adapter -- see that package's own doc.go.
// It needs to reach createSessionCore from outside this package, so this
// Step exports it (CreateSessionCore) and its error type (CreateSessionError,
// with exported Status/Message fields) -- a pure rename, no behavior
// change; every call site and test in THIS package keeps compiling
// unchanged (create.go's own doc comment above CreateSessionCore has the
// full reasoning).
//
// This Step also adds two other, independent pieces used by those same
// future ingress endpoints, neither wired to a concrete provider yet:
//
//   - internal/platform/webhooksig.go: a generic, provider-agnostic
//     raw-body HMAC-SHA256 signature-verification helper
//     (VerifyWebhookSignature / VerifyWebhookTimestamp) -- see that
//     file's own doc comment for the full GitHub-vs-Slack shape
//     reasoning and the HMACWebhookSecret design-question writeup.
//   - migrations/000027_webhook_deliveries.up.sql +
//     postgres.WebhookDeliveryStore.Claim: the atomic
//     INSERT ... ON CONFLICT dedupe/coalescing claim §5.1 calls for,
//     keyed on (provider, delivery_id).
package httpapi
