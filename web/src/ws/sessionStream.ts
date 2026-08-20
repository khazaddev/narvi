import type { QueryClient } from '@tanstack/react-query'
import type { FetchHistoryResponse, SubscribedPayload } from '@narvi/contracts/client-ws'

import type { BackoffOptions } from './transport'
import { ClientWsTransport } from './transport'
import { EventLog } from './eventLog'
import { initialSessionActivityState, reduceLog, type SessionActivityState } from './reducer'
import { invalidateForEvents } from './invalidation'
import type { ConnectionStatus, EventEnvelope, SyncState } from './types'
import { isPlainObject } from './util'

// SessionStream composes §12.1's full pipeline ("WS transport -> event log
// -> reducer -> query invalidation") into one object per viewed session.
// No view consumes this yet (Steps 81+ do, most likely via a
// `useSyncExternalStore(stream.subscribe, stream.getSnapshot)` hook built
// on top of subscribe()/getSnapshot() below) -- this Step's own job is
// making the pipeline itself real and correct, not rendering it.
//
// # Why a live broadcast triggers a fetch_history round-trip instead of
// being appended directly
//
// A live broadcast frame carries no id (transport.ts's own top comment,
// grounded in internal/app/ports/eventbroadcaster.go's doc comment: "sent
// exactly as stored... no separate wrapper envelope"). EventLog can only
// dedup/order things that have an id. Rather than inventing a synthetic
// local id for broadcast-sourced data (which would make it indistinguishable
// from real history on replay, or require its own separate reconciliation
// pass later), this class treats every live broadcast purely as a
// low-latency "something changed, go get the real thing" signal and
// answers it by calling fetch_history from the log's own current
// highestId() forward -- the SAME id-bearing, dedup-safe path used for the
// initial replay and for post-reconnect backfill. This is not a
// workaround: internal/app/ports/eventbroadcaster.go's own doc comment
// says exactly this is the intended recovery path ("any client can always
// recover a missed live push via fetch_history's cursor-paginated
// replay").
//
// # syncState and the "must not silently present a gap as complete" rule
//
// syncState only ever becomes 'complete' when a fetch_history round-trip
// itself returns nextCursor: null -- i.e. the SERVER has confirmed there
// is nothing between the log's own highestId() and "now". It is forced
// back to 'syncing' (never left at a stale 'complete') the instant the
// transport reports anything other than 'open' (onStatusChange below):
// a dropped connection means this client can no longer vouch for
// completeness until a fresh subscribe + backfill pass proves it again.
export interface SessionStreamSnapshot {
  connectionStatus: ConnectionStatus
  syncState: SyncState
  events: EventEnvelope[]
  activity: SessionActivityState
  lastError: string | null
}

export interface SessionStreamOptions {
  sessionId: string
  wsUrl: string
  clientId: string
  getToken: () => Promise<string>
  queryClient: QueryClient
  wsFactory?: (url: string) => WebSocket
  backoff?: BackoffOptions
  minFetchHistoryIntervalMs?: number
  fetchHistoryTimeoutMs?: number
  now?: () => number
}

function toEventEnvelope(raw: unknown): EventEnvelope | null {
  if (!isPlainObject(raw)) return null
  const { id, type, payload, createdAt } = raw
  if (typeof id !== 'number' || typeof type !== 'string' || typeof createdAt !== 'string') return null
  return { id, type, payload, createdAt }
}

/** parseEnvelopes converts a SubscribedPayload/FetchHistoryResponse `events` array (typed only as `{[k: string]: unknown}[]` by the generated schema -- see types.ts's own top comment for why) into EventEnvelopes, silently dropping (never throwing on) any element that doesn't shape up -- a malformed element from the server must not crash this pipeline, it must just not be trusted as log data. */
function parseEnvelopes(raw: readonly { [k: string]: unknown }[]): EventEnvelope[] {
  const out: EventEnvelope[] = []
  for (const item of raw) {
    const envelope = toEventEnvelope(item)
    if (envelope !== null) out.push(envelope)
  }
  return out
}

export class SessionStream {
  private readonly log = new EventLog()
  private readonly transport: ClientWsTransport
  private readonly sessionId: string
  private readonly queryClient: QueryClient

  private connectionStatus: ConnectionStatus = 'idle'
  private syncState: SyncState = 'idle'
  private activity: SessionActivityState = initialSessionActivityState
  private lastError: string | null = null
  private readonly listeners = new Set<() => void>()
  private backfillInFlight = false
  private backfillDirty = false

  constructor(options: SessionStreamOptions) {
    this.sessionId = options.sessionId
    this.queryClient = options.queryClient
    this.transport = new ClientWsTransport({
      url: options.wsUrl,
      sessionId: options.sessionId,
      clientId: options.clientId,
      getToken: options.getToken,
      wsFactory: options.wsFactory,
      backoff: options.backoff,
      minFetchHistoryIntervalMs: options.minFetchHistoryIntervalMs,
      fetchHistoryTimeoutMs: options.fetchHistoryTimeoutMs,
      now: options.now,
      handlers: {
        onStatusChange: (status) => {
          this.connectionStatus = status
          if (status !== 'open' && this.syncState !== 'idle') {
            this.syncState = 'syncing'
          }
          this.notify()
        },
        onSubscribed: (payload) => {
          this.handleSubscribed(payload)
        },
        onBroadcast: () => {
          this.handleBroadcast()
        },
        onProtocolError: (message) => {
          this.lastError = message
          this.notify()
        },
      },
    })
  }

  start(): void {
    this.transport.start()
  }

  stop(): void {
    this.transport.stop()
  }

  getSnapshot(): SessionStreamSnapshot {
    return {
      connectionStatus: this.connectionStatus,
      syncState: this.syncState,
      events: this.log.entries(),
      activity: this.activity,
      lastError: this.lastError,
    }
  }

  subscribe(listener: () => void): () => void {
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
    }
  }

  private notify(): void {
    for (const listener of this.listeners) listener()
  }

  private handleSubscribed(payload: SubscribedPayload): void {
    const inserted = this.log.appendMany(parseEnvelopes(payload.events))
    this.applyNewEvents(inserted)
    // Always run at least one backfill pass after a (re)subscribe: the
    // initial replay is capped (client.go's own initialReplayLimit=200 /
    // maxInitialReplayBytes=16KiB) with no signal in the payload telling
    // this client whether it was actually truncated -- see this file's
    // own top comment and web/README.md's own "Reconnect gaps" section.
    // The only way to know for certain is to ask.
    void this.runBackfill()
  }

  private handleBroadcast(): void {
    void this.runBackfill()
  }

  private applyNewEvents(inserted: EventEnvelope[]): void {
    if (inserted.length > 0) {
      this.activity = reduceLog(this.log.entries())
      invalidateForEvents(this.queryClient, this.sessionId, inserted)
    }
    this.notify()
  }

  private async runBackfill(): Promise<void> {
    if (this.backfillInFlight) {
      // A pass is already walking forward; note that another is needed
      // once it finishes rather than firing a second, overlapping
      // fetch_history call (the transport only supports one in flight at
      // a time -- see transport.ts's own top comment).
      this.backfillDirty = true
      return
    }
    this.backfillInFlight = true
    try {
      for (;;) {
        this.setSyncState('syncing')
        const highest = this.log.highestId()
        const cursor = highest === null ? null : String(highest)
        let response: FetchHistoryResponse
        try {
          response = await this.transport.fetchHistory(cursor)
        } catch (err) {
          // Connection dropped, or the request timed out (transport.ts's
          // own fetchHistoryTimeoutMs) -- stop this pass. Do not spin
          // retrying against a connection that is either already
          // reconnecting (transport owns that backoff) or already dead;
          // a fresh 'subscribed' reply, once it arrives, starts a new
          // backfill pass on its own (handleSubscribed above).
          this.lastError = err instanceof Error ? err.message : String(err)
          this.notify()
          return
        }
        const inserted = this.log.appendMany(parseEnvelopes(response.events))
        this.applyNewEvents(inserted)
        if (response.nextCursor === null) break
      }
      this.setSyncState('complete')
    } finally {
      this.backfillInFlight = false
      if (this.backfillDirty) {
        this.backfillDirty = false
        void this.runBackfill()
      }
    }
  }

  private setSyncState(state: SyncState): void {
    if (this.syncState === state) return
    this.syncState = state
    this.notify()
  }
}
