import { QueryClient } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { sessionQueryKeys } from '../../api/queryKeys'
import { SessionStream } from '../sessionStream'
import { FakeClientWsServer, type FakeConnection, fakeEvent, subscribedPayload } from './fakeServer'

// sessionStream.test.ts drives the REAL SessionStream (real ClientWsTransport
// + real EventLog + real reduceLog + real invalidateForEvents) against a
// real local fake server -- these are the pipeline-level tests: they pin
// dedup, gap-filling/backfill, and invalidation composing correctly END TO
// END, not any one stage in isolation (eventLog.test.ts/reducer.test.ts/
// invalidation.test.ts already cover each stage on its own).

async function waitFor(predicate: () => boolean, timeoutMs = 5000): Promise<void> {
  const start = Date.now()
  while (!predicate()) {
    if (Date.now() - start > timeoutMs) {
      throw new Error('waitFor: timed out waiting for condition')
    }
    await new Promise((resolve) => setTimeout(resolve, 5))
  }
}

/** drainOneBackfillRound reads the next inbound frame (expected to be a fetch_history request) and replies with `events`/`nextCursor`. */
async function drainOneBackfillRound(conn: FakeConnection, events: ReturnType<typeof fakeEvent>[], nextCursor: string | null): Promise<unknown> {
  const request = await conn.nextMessage()
  conn.send({ events, nextCursor })
  return request
}

let server: FakeClientWsServer | undefined
let stream: SessionStream | undefined

afterEach(async () => {
  stream?.stop()
  stream = undefined
  await server?.close()
  server = undefined
})

function newStream(sessionId: string, queryClient: QueryClient): SessionStream {
  return new SessionStream({
    sessionId,
    wsUrl: server!.urlFor(sessionId),
    clientId: 'client-1',
    getToken: () => Promise.resolve('tok'),
    queryClient,
    minFetchHistoryIntervalMs: 0,
    backoff: { initialMs: 5, maxMs: 20, factor: 2 },
  })
}

describe('SessionStream', () => {
  it('a redelivered replay (every fresh subscribe re-sends the same events, reconnect included) is applied exactly once -- removing EventLog\'s dedup guard makes this test fail', async () => {
    server = await FakeClientWsServer.start()
    const queryClient = new QueryClient()
    stream = newStream('sess-1', queryClient)

    const firstConnPromise = server.waitForConnection()
    stream.start()
    const firstConn = await firstConnPromise
    await firstConn.nextMessage()
    firstConn.send(subscribedPayload('sess-1', [fakeEvent(1), fakeEvent(2)]))
    await drainOneBackfillRound(firstConn, [], null)
    await waitFor(() => stream!.getSnapshot().syncState === 'complete')
    expect(stream.getSnapshot().activity.eventCount).toBe(2)

    // Reconnect: the server drops the connection; the transport reconnects
    // on its own backoff, and the fresh subscribe RE-SENDS the identical
    // events [1, 2] -- exactly what wshub/client.go's own afterID=0 replay
    // does on every single subscribe, reconnect included (confirmed by
    // reading that file directly; see web/README.md's own "Redelivery"
    // section).
    const secondConnPromise = server.waitForConnection()
    firstConn.close(1011, 'simulated drop')
    const secondConn = await secondConnPromise
    await secondConn.nextMessage()
    secondConn.send(subscribedPayload('sess-1', [fakeEvent(1), fakeEvent(2)]))
    await drainOneBackfillRound(secondConn, [], null)

    await waitFor(() => stream!.getSnapshot().connectionStatus === 'open' && stream!.getSnapshot().syncState === 'complete')
    const snapshot = stream.getSnapshot()
    expect(snapshot.events.map((e) => e.id)).toEqual([1, 2])
    // NOT 4: a redelivered event must not double-apply.
    expect(snapshot.activity.eventCount).toBe(2)
    expect(snapshot.activity.countsByType).toEqual({ tool_call: 2 })
  })

  it('a reconnect never silently drops an event that was added while disconnected', async () => {
    server = await FakeClientWsServer.start()
    const queryClient = new QueryClient()
    stream = newStream('sess-2', queryClient)

    const firstConnPromise = server.waitForConnection()
    stream.start()
    const firstConn = await firstConnPromise
    await firstConn.nextMessage()
    firstConn.send(subscribedPayload('sess-2', [fakeEvent(1)]))
    await drainOneBackfillRound(firstConn, [], null)
    await waitFor(() => stream!.getSnapshot().syncState === 'complete')

    // While "disconnected", a new event (id 2) was durably committed on
    // the server. The reconnect's fresh subscribe reply reflects it.
    const secondConnPromise = server.waitForConnection()
    firstConn.close(1011, 'simulated drop')
    const secondConn = await secondConnPromise
    await secondConn.nextMessage()
    secondConn.send(subscribedPayload('sess-2', [fakeEvent(1), fakeEvent(2)]))
    await drainOneBackfillRound(secondConn, [], null)

    await waitFor(() => stream!.getSnapshot().connectionStatus === 'open' && stream!.getSnapshot().syncState === 'complete')
    expect(stream.getSnapshot().events.map((e) => e.id)).toEqual([1, 2])
  })

  it('syncState stays "syncing" (never "complete") while a truncated initial replay is still being backfilled via fetch_history, and the log ends up gap-free', async () => {
    server = await FakeClientWsServer.start()
    const queryClient = new QueryClient()
    stream = newStream('sess-3', queryClient)

    const connPromise = server.waitForConnection()
    stream.start()
    const conn = await connPromise
    await conn.nextMessage()
    // Simulates client.go's own initialReplayLimit/maxInitialReplayBytes
    // truncation: only event 1 comes back in the subscribed reply, even
    // though the session's real history is longer.
    conn.send(subscribedPayload('sess-3', [fakeEvent(1)]))

    const fh1 = await conn.nextMessage()
    expect(fh1).toMatchObject({ type: 'fetch_history', sessionId: 'sess-3', cursor: '1' })
    expect(stream.getSnapshot().syncState).toBe('syncing')
    conn.send({ events: [fakeEvent(2)], nextCursor: '2' })

    await waitFor(() => stream!.getSnapshot().events.length === 2)
    // A page with a non-null nextCursor means more history remains --
    // syncState must NOT claim completeness yet.
    expect(stream.getSnapshot().syncState).toBe('syncing')

    const fh2 = await conn.nextMessage()
    expect(fh2).toMatchObject({ cursor: '2' })
    conn.send({ events: [fakeEvent(3)], nextCursor: null })

    await waitFor(() => stream!.getSnapshot().syncState === 'complete')
    expect(stream.getSnapshot().events.map((e) => e.id)).toEqual([1, 2, 3])
  })

  it('a live broadcast (no id, no envelope) triggers a fresh fetch_history round rather than being trusted as log data directly', async () => {
    server = await FakeClientWsServer.start()
    const queryClient = new QueryClient()
    stream = newStream('sess-4', queryClient)

    const connPromise = server.waitForConnection()
    stream.start()
    const conn = await connPromise
    await conn.nextMessage()
    conn.send(subscribedPayload('sess-4', [fakeEvent(1)]))
    await drainOneBackfillRound(conn, [], null)
    await waitFor(() => stream!.getSnapshot().syncState === 'complete')

    // An anonymous live broadcast frame -- exactly the shape
    // internal/app/ports/eventbroadcaster.go's own doc comment describes
    // (the bare events.payload column, no id/type wrapper).
    conn.send({ note: 'something changed' })

    await waitFor(() => stream!.getSnapshot().syncState === 'syncing')
    await drainOneBackfillRound(conn, [fakeEvent(2)], null)

    await waitFor(() => stream!.getSnapshot().events.length === 2)
    await waitFor(() => stream!.getSnapshot().syncState === 'complete')
    expect(stream.getSnapshot().events.map((e) => e.id)).toEqual([1, 2])
  })

  it('a malformed element inside an otherwise-valid events array is dropped, not applied and not fatal -- its valid siblings still land', async () => {
    server = await FakeClientWsServer.start()
    const queryClient = new QueryClient()
    stream = newStream('sess-5', queryClient)

    const connPromise = server.waitForConnection()
    stream.start()
    const conn = await connPromise
    await conn.nextMessage()
    conn.send(subscribedPayload('sess-5', [fakeEvent(1), { id: 'not-a-number', type: 'tool_call', payload: {}, createdAt: 'x' } as never, fakeEvent(2)]))
    await drainOneBackfillRound(conn, [], null)

    await waitFor(() => stream!.getSnapshot().syncState === 'complete')
    expect(stream.getSnapshot().events.map((e) => e.id)).toEqual([1, 2])
  })

  it('newly-applied events invalidate the session\'s query keys -- breaking invalidateForEvents makes this test fail', async () => {
    server = await FakeClientWsServer.start()
    const queryClient = new QueryClient()
    const spy = vi.spyOn(queryClient, 'invalidateQueries')
    stream = newStream('sess-6', queryClient)

    const connPromise = server.waitForConnection()
    stream.start()
    const conn = await connPromise
    await conn.nextMessage()
    conn.send(subscribedPayload('sess-6', [fakeEvent(1, 'execution_complete')]))
    await drainOneBackfillRound(conn, [], null)

    await waitFor(() => stream!.getSnapshot().syncState === 'complete')
    const invalidatedKeys = spy.mock.calls.map((call) => call[0]?.queryKey)
    expect(invalidatedKeys).toContainEqual(sessionQueryKeys.detail('sess-6'))
    expect(invalidatedKeys).toContainEqual(sessionQueryKeys.events('sess-6'))
  })

  it('connectionStatus/syncState downgrade to "syncing" the instant the connection drops, and never claim "complete" mid-outage', async () => {
    server = await FakeClientWsServer.start()
    const queryClient = new QueryClient()
    stream = newStream('sess-7', queryClient)

    const firstConnPromise = server.waitForConnection()
    stream.start()
    const firstConn = await firstConnPromise
    await firstConn.nextMessage()
    firstConn.send(subscribedPayload('sess-7', [fakeEvent(1)]))
    await drainOneBackfillRound(firstConn, [], null)
    await waitFor(() => stream!.getSnapshot().syncState === 'complete')

    const secondConnPromise = server.waitForConnection()
    firstConn.close(1011, 'simulated drop')

    await waitFor(() => stream!.getSnapshot().syncState !== 'complete')
    expect(stream.getSnapshot().connectionStatus).not.toBe('open')

    const secondConn = await secondConnPromise
    await secondConn.nextMessage()
    secondConn.send(subscribedPayload('sess-7', [fakeEvent(1)]))
    await drainOneBackfillRound(secondConn, [], null)
    await waitFor(() => stream!.getSnapshot().syncState === 'complete')
  })
})
