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
// CreateSessionRequest.prompt is still accepted and parsed but NOT acted
// upon -- dispatching a first turn needs a real sandbox spawn, Step 21's
// job ("e2e happy path"), not this one. Wiring participants/presence
// (§8.11) is still untouched -- a distinct, not-yet-scoped concern, now
// simply for a cleaner reason ("real user identity exists as of this Step,
// but presence/multiplayer wiring is its own job") rather than "no auth
// mechanism exists yet at all".
package httpapi
