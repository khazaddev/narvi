import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError, request } from '../http'

afterEach(() => {
  vi.unstubAllGlobals()
})

function respondWith(body: string | null, init: ResponseInit): void {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(body, init)),
  )
}

describe('request', () => {
  // The error branch already wrapped its own json() call in a try/catch; the
  // success branch did not. That asymmetry was the defect: a 200 whose body is
  // not JSON is exactly what a reverse proxy, a captive portal, a maintenance
  // page or a misrouted request returns, and an unguarded parse threw past
  // every caller into the app's error boundary.
  //
  // Found by running the app in a browser rather than by reading it: with no
  // dev proxy configured, Vite answered /api/me with index.html, and the app
  // rendered "Unexpected token '<', "<!doctype "... is not valid JSON" in place
  // of the sign-in screen. It lands hardest on the very first request the app
  // makes, so a visitor could not reach sign-in at all.
  it('surfaces a non-JSON 200 as a typed ApiError rather than a raw parse error', async () => {
    respondWith('<!doctype html><html><body>maintenance</body></html>', {
      status: 200,
      headers: { 'content-type': 'text/html' },
    })

    await expect(request<{ id: string }>('/api/me')).rejects.toBeInstanceOf(ApiError)
  })

  it('names the non-JSON body in the error, so the cause is visible rather than a parser message', async () => {
    respondWith('not json at all', { status: 200 })

    await expect(request<{ id: string }>('/api/me')).rejects.toThrow(/non-JSON body/)
  })

  // The behaviour the guard must NOT change: a well-formed JSON body still
  // parses and returns, so this is a guard against a failure mode rather than
  // a new code path every response now walks.
  it('still returns a parsed body when the response really is JSON', async () => {
    respondWith(JSON.stringify({ id: 'user-1' }), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    })

    await expect(request<{ id: string }>('/api/me')).resolves.toEqual({ id: 'user-1' })
  })

  // A 204 carries no body at all and must not be parsed — asserted here
  // because the guard sits immediately after that branch and a careless edit
  // could fold the two together.
  it('returns undefined for 204 without attempting to parse a body', async () => {
    // 204 is a null-body status: the Response constructor rejects a body
    // for it outright, which is itself a reminder of why request() must not
    // try to parse one.
    respondWith(null, { status: 204 })

    await expect(request<undefined>('/api/logout')).resolves.toBeUndefined()
  })
})
