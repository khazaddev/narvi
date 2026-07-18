// This file (client.go) implements the CLIENT half of the WS hub (§6.2):
// the browser <-> control-plane protocol, as opposed to sandbox.go's
// server-side mirror of the sandbox-agent <-> control-plane protocol
// (§6.1, Step 18). It shares this package's route
// (GET /sessions/{sessionID}/ws) with the sandbox socket, discriminated by
// ?type=client vs ?type=sandbox via handler.go's own NewHandler
// dispatcher -- see that file's own doc comment.
//
// # Client-handshake outcome table
//
//  1. sessionID path param does not parse as a UUID -> 404 (mirrors the
//     sandbox handler's own established convention exactly -- see
//     sandbox.go's own doc comment for why a malformed id and a
//     nonexistent one are treated identically; §6.2 does not itself
//     mandate a pre-upgrade status code here, this is a deliberate,
//     reasoned UX choice in the same style).
//  2. session row not found (pgx.ErrNoRows) -> 404; any other lookup
//     error -> 500.
//  3. websocket.Accept(w, r, &websocket.AcceptOptions{}) -- deliberately
//     NO InsecureSkipVerify, unlike sandbox.go's own Accept call. The
//     sandbox socket is a non-browser, server-to-server, bearer-
//     authenticated connection, so disabling coder/websocket's Origin-
//     header same-origin check there is correct (see sandbox.go's own
//     comment). THIS socket is browser-facing: a malicious webpage
//     attempting a cross-origin WS connection using a victim's browser
//     session is exactly the CSRF-class attack that same-origin
//     enforcement exists to stop, and narvi serves API+WS+UI on one
//     origin (§12.1: "one binary... on one port"), so legitimate
//     same-origin browser connections are unaffected by leaving the
//     default enforcement on. Do not copy sandbox.go's InsecureSkipVerify
//     here.
//  4. The first inbound message, bounded by
//     platform.Timeouts.ClientSubscribeTimeout (§6.2: "within 30s"):
//     missing (deadline elapses first), a read error, or malformed JSON
//     -> conn.Close(4001, "re-auth required").
//  5. The parsed clientws.SubscribeRequest's token: looked up via
//     wsTokens.GetByHash(platform.HashToken(token)). Not found, or found
//     for a DIFFERENT session than the URL's own sessionID -> close 4001
//     (both are treated as "not a valid credential for THIS session",
//     never leaking which case it was). Found, for this session, but
//     expired (expires_at before now) -> close 4002 ("token expired").
//  6. Otherwise: assemble and write a single `subscribed` reply
//     (SubscribedPayload), register the connection with the shared *Hub
//     for live broadcast delivery, then run the post-subscribe read loop
//     (fetch_history) until the connection errs or closes.
//
// No registry.GetOrSpawn call belongs anywhere in this handshake: every
// step above is a plain store read (session/ws-token/turns/sandbox/
// events/artifacts), matching the sandbox handler's own precedent that
// the actor need not exist just to satisfy a read. The session's Actor
// hydrates lazily whenever something ELSE (a sandbox event, a timer)
// first needs it; a browser subscribing is not that trigger.

package wshub

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sync/errgroup"

	"github.com/khazaddev/narvi/contracts/gen/go/clientws"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/platform"
)

// initialReplayLimit bounds how many of a session's oldest events are
// included directly in the SubscribedPayload's own "events" field at
// subscribe time (§6.2's own design-decision note: "older history is
// available via fetch_history" -- this is a fixed, documented limit, not
// an attempt to replay a session's full history unboundedly). A plain
// count, not a duration, so (like sessionactor's own mailboxBufferSize) it
// is an ordinary Go constant, not a platform.Timeouts field.
const initialReplayLimit = 200

// maxInitialReplayBytes bounds the TOTAL marshaled size of the events
// included in one SubscribedPayload, independent of initialReplayLimit's
// own item-count cap -- item count alone is not enough: coder/websocket's
// own default per-message read limit is 32768 bytes (Conn.SetReadLimit's
// own doc), and this Step's own protocol negotiates no larger limit on
// either side, so a session containing initialReplayLimit events with
// large individual payloads (e.g. sizeable tool_call/tool_result data) can
// produce a subscribed reply that exceeds a stock client's own default and
// kills the ENTIRE handshake with ErrMessageTooBig, not just truncate the
// replay. Chosen conservatively below that 32KiB wire default, leaving
// headroom for the rest of the payload (state/artifacts/sessionId/
// participants). At least one event is always kept even if it alone
// exceeds this budget -- truncating to zero would silently hide real
// content while providing no size guarantee anyway, and a client that
// needs the rest of a large session's history already has fetch_history.
const maxInitialReplayBytes = 16 * 1024

// fetchHistoryDefaultLimit / fetchHistoryMaxLimit bound a fetch_history
// request's own page size: the client's requested Limit is honored up to
// fetchHistoryMaxLimit; a nil/absent Limit uses fetchHistoryDefaultLimit
// (FetchHistoryRequest.Limit's own doc: "Null means use the server
// default page size"). Plain counts, not durations.
const (
	fetchHistoryDefaultLimit = 100
	fetchHistoryMaxLimit     = 500
)

// NewClientHandler builds the HTTP handler backing GET
// /sessions/{sessionID}/ws?type=client (§6.2) -- this file's own top
// comment has the full handshake outcome table.
//
// registry is accepted for signature symmetry with NewSandboxHandler and
// for a later Step's likely need to route a client-originated command
// through the session's own Actor -- this Step's own handshake is
// entirely read-only (see this file's top comment) and does not call
// GetOrSpawn.
func NewClientHandler(
	registry *sessionactor.Registry,
	sessions *postgres.SessionStore,
	turns *postgres.TurnStore,
	sandboxes *postgres.SandboxStore,
	events *postgres.EventStore,
	artifacts *postgres.ArtifactStore,
	wsTokens *postgres.WSTokenStore,
	hub *Hub,
	timeouts platform.Timeouts,
) http.HandlerFunc {
	_ = registry // see doc comment above: not used by this Step's own read-only handshake.

	return func(w http.ResponseWriter, r *http.Request) {
		// (1) sessionID path param -> pgtype.UUID.
		rawSessionID := chi.URLParam(r, "sessionID")
		var sessionID pgtype.UUID
		if err := sessionID.Scan(rawSessionID); err != nil {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}

		ctx := platform.WithSessionID(r.Context(), sessionID.String())
		logger := platform.Logger(ctx)

		// (2) session row lookup.
		sessionRow, err := sessions.Get(ctx, sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				http.Error(w, "session not found", http.StatusNotFound)
				return
			}
			logger.Error("wshub: get session failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		// (3) Accept -- deliberately NO InsecureSkipVerify; see this
		// file's own top comment for the full contrast with sandbox.go.
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
		if err != nil {
			logger.Error("wshub: client websocket accept failed", "error", err)
			return
		}
		defer func() { _ = conn.CloseNow() }()

		// (4) the first inbound message, bounded by ClientSubscribeTimeout.
		req, ok := readSubscribeRequest(ctx, conn, timeouts, logger)
		if !ok {
			return // readSubscribeRequest already closed the connection (4001).
		}

		// (5) verify the presented ws-token.
		if !verifyClientToken(ctx, conn, wsTokens, sessionID, req.Token, logger) {
			return // verifyClientToken already closed the connection (4001/4002).
		}

		// (6) assemble + write the single `subscribed` reply.
		payload, err := buildSubscribedPayload(ctx, sessionID, sessionRow, turns, sandboxes, events, artifacts)
		if err != nil {
			logger.Error("wshub: assembling subscribed payload failed", "error", err)
			_ = conn.Close(websocket.StatusInternalError, "internal error")
			return
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			logger.Error("wshub: marshal subscribed payload failed", "error", err)
			_ = conn.Close(websocket.StatusInternalError, "internal error")
			return
		}
		if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
			logger.Warn("wshub: write subscribed payload failed", "error", err)
			return
		}

		logger.Info("wshub: client ws subscribed", "client_id", req.ClientId)

		// (7) register for live broadcast delivery -- the writer goroutine
		// this starts is scoped to writerCtx/writerGroup, both torn down
		// (canceled + joined) before this handler returns, so no goroutine
		// outlives the connection it serves.
		writerCtx, cancelWriter := context.WithCancel(ctx)
		defer cancelWriter()
		var writerGroup errgroup.Group
		unregister := hub.Register(writerCtx, &writerGroup, sessionID.String(), conn)
		defer func() {
			unregister()
			cancelWriter()
			_ = writerGroup.Wait()
		}()

		// (8) post-subscribe read loop (fetch_history).
		readClientLoop(ctx, conn, sessionID, events, logger)
	}
}

// readSubscribeRequest reads and parses the first inbound frame, bounded
// by timeouts.ClientSubscribeTimeout (§6.2: "within 30s"). The blocking
// conn.Read call runs on its own goroutine (via errgroup.Group.Go -- §11,
// no naked goroutines) so this function can race it against a plain
// time.After timer instead of handing conn.Read itself a context deadline
// -- coder/websocket arms its OWN deadline-driven close (via
// context.AfterFunc) the instant a context passed to Read carries a
// deadline, which would force ITS OWN close code/reason instead of the
// 4001 this function needs to send deliberately (mirrors dispatch_test.
// go's own startReader precedent and its documented reasoning for the
// exact same coder/websocket quirk). ok is false in every case this
// function has already closed conn itself (4001) -- the caller must
// simply return.
func readSubscribeRequest(ctx context.Context, conn *websocket.Conn, timeouts platform.Timeouts, logger *slog.Logger) (clientws.SubscribeRequest, bool) {
	type frame struct {
		data []byte
		err  error
	}
	frameCh := make(chan frame, 1)

	var eg errgroup.Group
	eg.Go(func() error {
		_, data, err := conn.Read(ctx)
		frameCh <- frame{data: data, err: err}
		return nil
	})

	var req clientws.SubscribeRequest

	select {
	case f := <-frameCh:
		if f.err != nil {
			logger.Warn("wshub: client subscribe: read error before a subscribe frame arrived; closing 4001", "error", f.err)
			_ = conn.Close(websocket.StatusCode(4001), "re-auth required")
			_ = eg.Wait()
			return req, false
		}
		if err := json.Unmarshal(f.data, &req); err != nil {
			logger.Warn("wshub: client subscribe: malformed subscribe frame; closing 4001", "error", err)
			_ = conn.Close(websocket.StatusCode(4001), "re-auth required")
			_ = eg.Wait()
			return req, false
		}
		_ = eg.Wait()
		return req, true

	case <-time.After(timeouts.ClientSubscribeTimeout):
		logger.Warn("wshub: client subscribe: no subscribe frame within timeout; closing 4001")
		_ = conn.Close(websocket.StatusCode(4001), "re-auth required")
		_ = eg.Wait() // conn is now closed; the blocked Read above returns an error and the goroutine exits.
		return req, false
	}
}

// verifyClientToken looks up token's hash and, per this file's own
// handshake outcome table: closes 4001 if no matching row exists or the
// row belongs to a different session than sessionID (both cases are
// deliberately indistinguishable to the caller -- never leak which);
// closes 4002 if the row is a genuine match but already expired;
// otherwise returns true (proceed) with the connection untouched. A
// genuine backend error during the lookup (e.g. a transient Postgres
// blip) is NOT folded into 4001 -- telling every subscribing client its
// credential is bad during an unrelated infrastructure hiccup would be
// actively misleading -- it closes with StatusInternalError instead,
// matching how the rest of this file's own error paths (e.g.
// buildSubscribedPayload's) already distinguish "not found" from "we
// failed to check".
func verifyClientToken(ctx context.Context, conn *websocket.Conn, wsTokens *postgres.WSTokenStore, sessionID pgtype.UUID, token string, logger *slog.Logger) bool {
	row, err := wsTokens.GetByHash(ctx, platform.HashToken(token))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			logger.Warn("wshub: client subscribe: unknown ws token; closing 4001")
			_ = conn.Close(websocket.StatusCode(4001), "re-auth required")
			return false
		}
		logger.Error("wshub: ws-token lookup failed", "error", err)
		_ = conn.Close(websocket.StatusInternalError, "internal error")
		return false
	}
	if row.SessionID != sessionID {
		logger.Warn("wshub: client subscribe: ws token belongs to a different session; closing 4001")
		_ = conn.Close(websocket.StatusCode(4001), "re-auth required")
		return false
	}
	if time.Now().After(row.ExpiresAt.Time) {
		logger.Warn("wshub: client subscribe: expired ws token; closing 4002")
		_ = conn.Close(websocket.StatusCode(4002), "token expired")
		return false
	}
	return true
}

// buildSubscribedPayload assembles the single reply to subscribe (§6.2):
// a genuinely minimal, honestly-scoped state map (session + turns +
// sandbox-or-nil -- the full read-model shape is explicitly left to later
// PRs by the schema's own doc comment), the first initialReplayLimit
// events, every artifact (unbounded -- expected to stay small), and an
// always-empty participants array (design decision: participants stays
// completely untouched this Step, see this package's own doc.go).
func buildSubscribedPayload(
	ctx context.Context,
	sessionID pgtype.UUID,
	sessionRow sqlcgen.Session,
	turns *postgres.TurnStore,
	sandboxes *postgres.SandboxStore,
	events *postgres.EventStore,
	artifacts *postgres.ArtifactStore,
) (clientws.SubscribedPayload, error) {
	turnRows, err := turns.ListForSession(ctx, sessionID)
	if err != nil {
		return clientws.SubscribedPayload{}, err
	}
	if turnRows == nil {
		turnRows = []sqlcgen.Turn{}
	}

	var sandboxState any
	sandboxRow, err := sandboxes.Get(ctx, sessionID)
	switch {
	case err == nil:
		sandboxState = sandboxRow
	case errors.Is(err, pgx.ErrNoRows):
		sandboxState = nil
	default:
		return clientws.SubscribedPayload{}, err
	}

	eventRows, err := events.ListForSession(ctx, sessionID, 0, initialReplayLimit)
	if err != nil {
		return clientws.SubscribedPayload{}, err
	}

	artifactRows, err := artifacts.ListForSession(ctx, sessionID)
	if err != nil {
		return clientws.SubscribedPayload{}, err
	}

	wireEvents := truncateEventsToByteBudget(subscribedEventsWire(eventRows), maxInitialReplayBytes)

	return clientws.SubscribedPayload{
		SessionId: sessionID.String(),
		State: clientws.SubscribedPayloadState{
			"session": sessionRow,
			"turns":   turnRows,
			"sandbox": sandboxState,
		},
		Events:       wireEvents,
		Artifacts:    subscribedArtifactsWire(artifactRows),
		Participants: []clientws.SubscribedPayloadParticipantsElem{},
	}, nil
}

// truncateEventsToByteBudget returns the longest PREFIX of wire (order
// preserved) whose total marshaled JSON size stays within maxBytes, always
// keeping at least the first element regardless of its own size (see
// maxInitialReplayBytes's own doc comment for why). A marshal failure on
// any one element is treated as zero-size for this size-accounting pass
// only -- the same element will surface that identical failure (fatally,
// correctly) when the whole SubscribedPayload is marshaled by this
// function's own caller, so silently skipping it HERE does not hide a
// real error, only defers where it is reported.
func truncateEventsToByteBudget(wire []clientws.SubscribedPayloadEventsElem, maxBytes int) []clientws.SubscribedPayloadEventsElem {
	if len(wire) == 0 {
		return wire
	}
	total := 0
	for i, ev := range wire {
		b, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		total += len(b)
		if total > maxBytes && i > 0 {
			return wire[:i]
		}
	}
	return wire
}

// eventWireMap builds this package's own deliberately minimal wire shape
// for one stored event row: id (for client-side de-dup against live
// broadcasts), type (needed to interpret payload -- unlike a live
// broadcast frame, a synthetic actor-appended event's own raw payload
// does not always embed its own type, see sessionactor's own appendEvent),
// payload (embedded as real JSON via json.RawMessage, never
// base64-encoded -- sqlcgen.Event.Payload is a plain []byte, which
// encoding/json would otherwise base64-encode by default), and createdAt.
// clientws's own protocol.schema.json declares these array elements
// additionalProperties:true -- this shape is this package's own choice,
// not a wire-contract requirement -- shared by the subscribe-time replay
// and every fetch_history reply so a client never needs two parsing paths
// for "replayed" vs "paginated" history.
func eventWireMap(e sqlcgen.Event) map[string]interface{} {
	return map[string]interface{}{
		"id":        e.ID,
		"type":      e.Type,
		"payload":   json.RawMessage(e.Payload),
		"createdAt": e.CreatedAt,
	}
}

// artifactWireMap is eventWireMap's own artifact counterpart --
// sqlcgen.Artifact.Metadata is likewise a plain []byte needing the same
// json.RawMessage treatment to avoid base64-encoding.
func artifactWireMap(a sqlcgen.Artifact) map[string]interface{} {
	return map[string]interface{}{
		"id":        a.ID,
		"type":      a.Type,
		"url":       a.Url,
		"metadata":  json.RawMessage(a.Metadata),
		"createdAt": a.CreatedAt,
	}
}

func subscribedEventsWire(rows []sqlcgen.Event) []clientws.SubscribedPayloadEventsElem {
	wire := make([]clientws.SubscribedPayloadEventsElem, len(rows))
	for i, e := range rows {
		wire[i] = eventWireMap(e)
	}
	return wire
}

func subscribedArtifactsWire(rows []sqlcgen.Artifact) []clientws.SubscribedPayloadArtifactsElem {
	wire := make([]clientws.SubscribedPayloadArtifactsElem, len(rows))
	for i, a := range rows {
		wire[i] = artifactWireMap(a)
	}
	return wire
}

// clientEnvelope peeks just the "type" discriminator every post-subscribe
// client->CP frame carries (mirroring dispatch.go's own envelope-peek
// convention for the sandbox side). This Step's own protocol defines
// exactly one such shape, "fetch_history" -- an unrecognized type is
// logged and skipped, the same forward-compatible, deny-list-not-allow-
// list convention every other envelope-peek dispatcher in this codebase
// already uses.
type clientEnvelope struct {
	Type string `json:"type"`
}

// readClientLoop reads and dispatches inbound client-WS frames on conn
// until conn.Read errors (disconnect) or ctx is done. A read error simply
// ends the connection -- the caller's own deferred unregister/cancel
// already handle cleanup.
func readClientLoop(ctx context.Context, conn *websocket.Conn, sessionID pgtype.UUID, events *postgres.EventStore, logger *slog.Logger) {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}

		var env clientEnvelope
		if err := json.Unmarshal(data, &env); err != nil {
			logger.Warn("wshub: dropping malformed inbound client frame", "error", err)
			continue
		}

		switch env.Type {
		case "fetch_history":
			handleFetchHistory(ctx, conn, sessionID, events, data, logger)
		default:
			logger.Warn("wshub: ignoring unrecognized client frame type", "type", env.Type)
		}
	}
}

// handleFetchHistory parses data as a clientws.FetchHistoryRequest and
// replies with the requested page of this session's event log (§6.2).
// The URL's own sessionID is authoritative: a mismatched sessionId in the
// request body is rejected defensively (logged, not fatal -- the
// connection stays open) rather than trusted, since a WS connection is
// already scoped to exactly one session for its entire lifetime.
func handleFetchHistory(ctx context.Context, conn *websocket.Conn, sessionID pgtype.UUID, events *postgres.EventStore, data []byte, logger *slog.Logger) {
	var req clientws.FetchHistoryRequest
	if err := json.Unmarshal(data, &req); err != nil {
		logger.Warn("wshub: malformed fetch_history request", "error", err)
		return
	}
	if req.SessionId != "" && req.SessionId != sessionID.String() {
		logger.Warn("wshub: fetch_history sessionId mismatch; ignoring body value, using the connection's own session",
			"body_session_id", req.SessionId)
	}

	var afterID int64
	if req.Cursor != nil && *req.Cursor != "" {
		parsed, err := strconv.ParseInt(*req.Cursor, 10, 64)
		if err != nil {
			logger.Warn("wshub: malformed fetch_history cursor; treating as start", "cursor", *req.Cursor, "error", err)
		} else {
			afterID = parsed
		}
	}

	limit := fetchHistoryDefaultLimit
	if req.Limit != nil && *req.Limit > 0 {
		limit = *req.Limit
		if limit > fetchHistoryMaxLimit {
			limit = fetchHistoryMaxLimit
		}
	}

	rows, err := events.ListForSession(ctx, sessionID, afterID, int32(limit))
	if err != nil {
		logger.Error("wshub: fetch_history: list events failed", "error", err)
		return
	}

	wire := make([]clientws.FetchHistoryResponseEventsElem, len(rows))
	for i, e := range rows {
		wire[i] = eventWireMap(e)
	}

	var nextCursor *string
	if len(rows) == limit {
		s := strconv.FormatInt(rows[len(rows)-1].ID, 10)
		nextCursor = &s
	}

	resp := clientws.FetchHistoryResponse{
		Events:     wire,
		NextCursor: nextCursor,
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		logger.Error("wshub: marshal fetch_history response failed", "error", err)
		return
	}
	if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
		logger.Warn("wshub: write fetch_history response failed", "error", err)
	}
}

// hubConnBufferSize bounds how many not-yet-delivered broadcast payloads
// one registered connection's own channel holds before Hub.Broadcast
// starts dropping (and logging a warning) for it -- a plain count, not a
// duration, mirroring sessionactor's own mailboxBufferSize precedent for
// the same reason (not a platform.Timeouts field).
const hubConnBufferSize = 64

// Hub is the in-process, session-keyed registry of live client WS
// connections that makes ports.EventBroadcaster (and therefore §6.2's
// "→ broadcast stream") real. Constructed ONCE in cmd/control-plane/
// main.go and threaded through to BOTH sessionactor.NewRegistry (as the
// broadcaster every Actor's successful transact commits to, see
// internal/app/ports.EventBroadcaster's own doc comment) AND
// NewClientHandler (so it can register/unregister each subscribed
// connection) -- the single shared piece of state connecting the
// app-layer actor to the adapter-layer client sockets.
//
// Cross-pod broadcast fan-out is explicitly NOT solved here: a Hub only
// ever reaches connections registered in THIS process, the same class of
// honest, documented gap as Step 18's ErrSessionActorElsewhere/503
// stopgap (see this package's own doc.go) -- not a Postgres LISTEN/NOTIFY
// or message-bus solution, genuinely out of scope for this Step.
type Hub struct {
	mu    sync.Mutex
	conns map[string]map[*websocket.Conn]chan []byte
}

// NewHub builds an empty Hub.
func NewHub() *Hub {
	return &Hub{conns: make(map[string]map[*websocket.Conn]chan []byte)}
}

var _ ports.EventBroadcaster = (*Hub)(nil)

// Register starts conn's own dedicated writer goroutine (via eg.Go --
// §11, no naked goroutines) that drains newly broadcast payloads for
// sessionID and writes each one to conn verbatim (§6.2: a live-pushed
// event is the raw stored payload, sent exactly as stored -- no wrapper
// envelope), until writerCtx is done or a write fails. This goroutine's
// lifetime is scoped to writerCtx, which the caller (NewClientHandler)
// derives from the connection's own request context and cancels the
// moment that connection's read loop exits -- both exit together, as
// design decision 7g requires.
//
// The caller must call the returned unregister exactly once (typically
// deferred, alongside canceling writerCtx and joining eg) so this Hub
// stops fanning out to a connection that is going away.
func (h *Hub) Register(writerCtx context.Context, eg *errgroup.Group, sessionID string, conn *websocket.Conn) (unregister func()) {
	ch := make(chan []byte, hubConnBufferSize)

	h.mu.Lock()
	if h.conns[sessionID] == nil {
		h.conns[sessionID] = make(map[*websocket.Conn]chan []byte)
	}
	h.conns[sessionID][conn] = ch
	h.mu.Unlock()

	eg.Go(func() error {
		for {
			select {
			case <-writerCtx.Done():
				return nil
			case payload := <-ch:
				if err := conn.Write(writerCtx, websocket.MessageText, payload); err != nil {
					return nil // the connection's own read loop will independently notice and unregister.
				}
			}
		}
	})

	return func() {
		h.mu.Lock()
		delete(h.conns[sessionID], conn)
		if len(h.conns[sessionID]) == 0 {
			delete(h.conns, sessionID)
		}
		h.mu.Unlock()
	}
}

// Broadcast implements ports.EventBroadcaster: delivers payload to every
// connection currently registered for sessionID via a non-blocking send
// into that connection's own channel, dropping (and logging a warning)
// for any one connection whose buffer is already full -- this must never
// block the caller (the session actor's own single command-processing
// goroutine, see internal/app/sessionactor's Actor.broadcastPending) on a
// slow or non-draining browser client. A session with no registered
// connections (the common case -- most sessions have no live subscriber
// at any given instant) is a fast, silent no-op.
func (h *Hub) Broadcast(sessionID string, payload json.RawMessage) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, ch := range h.conns[sessionID] {
		select {
		case ch <- payload:
		default:
			platform.Logger(platform.WithSessionID(context.Background(), sessionID)).
				Warn("wshub: dropping broadcast for a slow/non-draining client connection")
		}
	}
}
