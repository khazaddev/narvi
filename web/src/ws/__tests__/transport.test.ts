import type { SubscribedPayload } from '@narvi/contracts/client-ws'
import { afterEach, describe, expect, it } from 'vitest'

import type { ConnectionStatus } from '../types'
import { ClientWsTransport } from '../transport'
import { FakeClientWsServer, fakeEvent, subscribeAndReply, subscribedPayload } from './fakeServer'

// transport.test.ts drives the REAL ClientWsTransport (production code,
// using the browser-standard global WebSocket client) against a real
// local `ws`-backed server (fakeServer.ts) -- see that file's own top
// comment for why nothing here is a stand-in for the transport itself.

let server: FakeClientWsServer | undefined
let transport: ClientWsTransport | undefined

afterEach(async () => {
  transport?.stop()
  transport = undefined
  await server?.close()
  server = undefined
})

describe('ClientWsTransport', () => {
  it('sends SubscribeRequest on open and delivers the server\'s SubscribedPayload to onSubscribed', async () => {
    server = await FakeClientWsServer.start()
    const events = [fakeEvent(1), fakeEvent(2)]
    const payload = subscribedPayload('sess-1', events)

    let resolveSubscribed = (_payload: SubscribedPayload): void => {}
    const subscribed = new Promise<SubscribedPayload>((resolve) => {
      resolveSubscribed = resolve
    })
    const localTransport = new ClientWsTransport({
      url: server.urlFor('sess-1'),
      sessionId: 'sess-1',
      clientId: 'client-1',
      getToken: () => Promise.resolve('tok-abc'),
      handlers: {
        onSubscribed: (payload) => resolveSubscribed(payload),
        onBroadcast: () => {},
        onStatusChange: () => {},
        onProtocolError: () => {},
      },
    })
    transport = localTransport

    const connPromise = server.waitForConnection()
    localTransport.start()
    const conn = await connPromise
    const subscribeFrame = (await conn.nextMessage()) as { token: string; clientId: string }
    expect(subscribeFrame).toEqual({ token: 'tok-abc', clientId: 'client-1' })
    conn.send(payload)

    const received = await subscribed
    expect(received.sessionId).toBe('sess-1')
    expect(received.events).toEqual(events)
  })

  it('reports status transitioning to "open" only once SubscribedPayload arrives, never before', async () => {
    server = await FakeClientWsServer.start()
    const statuses: ConnectionStatus[] = []

    transport = new ClientWsTransport({
      url: server.urlFor('sess-2'),
      sessionId: 'sess-2',
      clientId: 'client-1',
      getToken: () => Promise.resolve('tok'),
      handlers: {
        onSubscribed: () => {},
        onBroadcast: () => {},
        onStatusChange: (status) => statuses.push(status),
        onProtocolError: () => {},
      },
    })

    const connPromise = server.waitForConnection()
    transport.start()
    expect(statuses).toEqual(['connecting'])
    await subscribeAndReply(server, subscribedPayload('sess-2', []))
    await connPromise
    // Poll briefly for the async open->subscribed transition to land.
    for (let i = 0; i < 50 && !statuses.includes('open'); i++) {
      await new Promise((resolve) => setTimeout(resolve, 5))
    }
    expect(statuses).toEqual(['connecting', 'open'])
  })

  it('a malformed (non-JSON) frame after subscribe does not crash the transport -- it is reported via onProtocolError, and a subsequent valid broadcast still arrives', async () => {
    server = await FakeClientWsServer.start()
    const protocolErrors: string[] = []
    const broadcasts: unknown[] = []

    transport = new ClientWsTransport({
      url: server.urlFor('sess-3'),
      sessionId: 'sess-3',
      clientId: 'client-1',
      getToken: () => Promise.resolve('tok'),
      handlers: {
        onSubscribed: () => {},
        onBroadcast: (raw) => broadcasts.push(raw),
        onStatusChange: () => {},
        onProtocolError: (message) => protocolErrors.push(message),
      },
    })

    transport.start()
    const conn = await subscribeAndReply(server, subscribedPayload('sess-3', []))

    conn.sendRaw('not valid json {{{')
    conn.send({ hello: 'world' })

    for (let i = 0; i < 100 && broadcasts.length === 0; i++) {
      await new Promise((resolve) => setTimeout(resolve, 5))
    }

    expect(protocolErrors).toHaveLength(1)
    expect(protocolErrors[0]).toMatch(/malformed frame/)
    expect(broadcasts).toEqual([{ hello: 'world' }])
  })

  it('fetchHistory sends a correlated fetch_history request and resolves with the matching reply', async () => {
    server = await FakeClientWsServer.start()
    let resolveSubscribed = (): void => {}
    const subscribed = new Promise<void>((resolve) => {
      resolveSubscribed = resolve
    })
    transport = new ClientWsTransport({
      url: server.urlFor('sess-4'),
      sessionId: 'sess-4',
      clientId: 'client-1',
      getToken: () => Promise.resolve('tok'),
      minFetchHistoryIntervalMs: 0,
      handlers: {
        onSubscribed: () => resolveSubscribed(),
        onBroadcast: () => {},
        onStatusChange: () => {},
        onProtocolError: () => {},
      },
    })
    transport.start()
    const conn = await subscribeAndReply(server, subscribedPayload('sess-4', []))
    // subscribeAndReply resolves once the SERVER has sent the subscribed
    // reply, not once the CLIENT has finished processing it -- fetchHistory
    // would otherwise race the transport's own handshake completion (it
    // rejects with "not connected" while awaitingSubscribedReply is still
    // true). Waiting for onSubscribed makes this deterministic.
    await subscribed

    const requestPromise = conn.nextMessage()
    const responsePromise = transport.fetchHistory('7', 50)
    const request = await requestPromise
    expect(request).toEqual({ type: 'fetch_history', sessionId: 'sess-4', cursor: '7', limit: 50 })
    conn.send({ events: [fakeEvent(8)], nextCursor: null })

    const response = await responsePromise
    expect(response).toEqual({ events: [fakeEvent(8)], nextCursor: null })
  })

  it('fetchHistory rejects after its own timeout if the server never replies (e.g. the server silently dropped it for arriving under the rate limit)', async () => {
    server = await FakeClientWsServer.start()
    let resolveSubscribed = (): void => {}
    const subscribed = new Promise<void>((resolve) => {
      resolveSubscribed = resolve
    })
    transport = new ClientWsTransport({
      url: server.urlFor('sess-5'),
      sessionId: 'sess-5',
      clientId: 'client-1',
      getToken: () => Promise.resolve('tok'),
      minFetchHistoryIntervalMs: 0,
      fetchHistoryTimeoutMs: 30,
      handlers: {
        onSubscribed: () => resolveSubscribed(),
        onBroadcast: () => {},
        onStatusChange: () => {},
        onProtocolError: () => {},
      },
    })
    transport.start()
    await subscribeAndReply(server, subscribedPayload('sess-5', []))
    await subscribed

    await expect(transport.fetchHistory(null)).rejects.toThrow(/timed out/)
  })

  it('reconnects with a fresh subscribe handshake after the server drops the connection', async () => {
    server = await FakeClientWsServer.start()
    const subscriptions: SubscribedPayload[] = []
    const statuses: ConnectionStatus[] = []

    transport = new ClientWsTransport({
      url: server.urlFor('sess-6'),
      sessionId: 'sess-6',
      clientId: 'client-1',
      getToken: () => Promise.resolve('tok'),
      backoff: { initialMs: 5, maxMs: 20, factor: 2 },
      handlers: {
        onSubscribed: (payload) => subscriptions.push(payload),
        onBroadcast: () => {},
        onStatusChange: (status) => statuses.push(status),
        onProtocolError: () => {},
      },
    })

    const firstConnPromise = server.waitForConnection()
    transport.start()
    const firstConn = await firstConnPromise
    await firstConn.nextMessage()
    firstConn.send(subscribedPayload('sess-6', [fakeEvent(1)]))
    while (subscriptions.length < 1) await new Promise((resolve) => setTimeout(resolve, 5))

    const secondConnPromise = server.waitForConnection()
    firstConn.close(1011, 'simulated drop')

    const secondConn = await secondConnPromise
    await secondConn.nextMessage()
    secondConn.send(subscribedPayload('sess-6', [fakeEvent(1), fakeEvent(2)]))
    while (subscriptions.length < 2) await new Promise((resolve) => setTimeout(resolve, 5))

    expect(subscriptions).toHaveLength(2)
    expect(subscriptions[1].events).toEqual([fakeEvent(1), fakeEvent(2)])
    expect(statuses).toContain('reconnecting')
  })
})
