// Local (non-generated) types for the §12.1 data layer's WS pipeline
// ("WS transport -> event log -> reducer -> query invalidation").
//
// Why these are hand-written and that is NOT a §12.1 violation: §12.1
// says "no hand-written response types anywhere" because /contracts is
// supposed to be the single source of truth for wire shapes. But
// contracts/client-ws/v1/protocol.schema.json's own SubscribedPayload and
// FetchHistoryResponse deliberately declare their `events` arrays as
// `additionalProperties: true` ("the full session/turn/sandbox read-model
// shape is assembled by later PRs" -- that schema's own top-level
// description) -- there is no generated name for "one replayed/paginated
// event" to reuse. The Go server has the identical gap and closes it the
// identical way: internal/adapters/inbound/wshub/client.go's own
// eventWireMap builds this exact {id, type, payload, createdAt} shape by
// hand, with a doc comment noting it is "this package's own choice, not a
// wire-contract requirement". EventEnvelope below is that same choice,
// made once on the client side instead of copy-pasted at every call site,
// and named so it can never collide with a real contracts/gen/ts export
// (web/scripts/check-no-dto-redeclaration.mjs enforces exactly that).
export interface EventEnvelope {
  /** events.id (bigserial, strictly monotonic per session) -- see reducer.ts's own top comment for why this, not arrival order, is what ordering is defined over. */
  id: number
  /** events.type, e.g. "execution_complete" | "tool_call" | "artifact" | "warning" | "heartbeat" (internal/app/sessionactor's own appendEvent/appendRawEvent call sites -- not a closed enum on the wire, so kept as `string`, not a union, matching the schema's own additionalProperties:true looseness). */
  type: string
  /** events.payload, verbatim (eventWireMap's own json.RawMessage(e.Payload) -- never base64, never re-encoded). */
  payload: unknown
  /** events.created_at, RFC3339. */
  createdAt: string
}

// ConnectionStatus is the WS transport's own connection lifecycle,
// reported independently of SyncState below -- see eventLog.ts's own top
// comment for why the two are deliberately never collapsed into one flag.
export type ConnectionStatus = 'idle' | 'connecting' | 'open' | 'reconnecting' | 'closed'

// SyncState is the event log's own claim about completeness:
//   - 'idle': no subscribe has ever completed yet.
//   - 'syncing': at least one gap is KNOWN to be open right now -- either
//     the initial replay may have been truncated (client.go's own
//     initialReplayLimit/maxInitialReplayBytes caps) and the fetch_history
//     backfill walk has not yet reached "no more pages", or a reconnect
//     has just happened and the post-reconnect backfill has not yet
//     confirmed the log is caught up again.
//   - 'complete': a fetch_history call has returned nextCursor: null,
//     proving (as of that round-trip) there is no gap left between the
//     log's own highest id and the session's true history.
// A consumer that renders "N events" without checking this can render a
// confidently-wrong number; see sessionStream.ts's own top comment.
export type SyncState = 'idle' | 'syncing' | 'complete'
