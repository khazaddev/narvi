// Package httpapi holds the REST adapter serving the web UI and external
// clients (§6.3): "The BFF-facing routes: sessions CRUD/create, events,
// artifacts, secrets, environments, automations, uploads, ws-token."
// Step 19's own plan row explicitly narrows that to exactly
// "create/get/events/artifacts" (+ ws-token, named separately by §6.2) --
// secrets/environments/automations/uploads are NOT this Step's job (see
// contracts/README.md's own scope note on rest/v1/dtos.schema.json).
//
// # Routes (all under /api/sessions, mounted by cmd/control-plane/main.go)
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
// # The auth gap -- resolved honestly, not faked (Step 20 owns the fix)
//
// No REST/cookie/OAuth auth middleware exists anywhere in this codebase
// yet -- every endpoint above is genuinely open/unauthenticated today.
// This is not worked around with a placeholder "system user":
//
//   - CreateSession always inserts with created_by: NULL (sessions.
//     created_by is nullable specifically for this class of gap -- see
//     migrations/000004_sessions.up.sql's own "bot/automation-created
//     sessions may have no direct human user" comment, broadened here to
//     also cover "no auth mechanism exists yet either").
//   - CreateSessionRequest.prompt is accepted and parsed but NOT acted
//     upon -- dispatching a first turn needs a real sandbox spawn, Step
//     21's job ("e2e happy path"), not this one.
//   - MintWSToken mints a token scoped to the SESSION only, not truly
//     "per-participant" in the RBAC sense yet (ws_tokens.user_id stays
//     NULL always) -- no real participant/user concept exists yet to
//     scope it to. Once Step 20 lands, whoever calls this endpoint would
//     be a real authenticated user via cookie session, and that identity
//     is what would populate user_id going forward.
package httpapi
