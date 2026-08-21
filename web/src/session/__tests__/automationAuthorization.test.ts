// automationAuthorization.test.ts -- the same "the affordance is not the
// real gate" proof planAuthorization.test.ts establishes for plan mode,
// applied to automations: createAutomation/resumeAutomation (api/
// endpoints.ts) are unconditional network calls with no client-side
// authorization logic of their own. The real gate is server-side
// (authz.ActionManageAutomations, admin/maintainer only,
// internal/adapters/inbound/httpapi/automations.go), already proven
// directly against a real Postgres instance by automations_integration_
// test.go's own TestCreateAutomation_MemberDenied. This file proves the
// client half: the request really goes out over the wire, and a 403
// surfaces as a genuine, typed error rather than being silently swallowed
// or treated as success.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { createAutomation, resumeAutomation } from '../../api/endpoints'
import { ApiError } from '../../api/http'

afterEach(() => {
  vi.unstubAllGlobals()
})

function respondWith(body: unknown, status: number): ReturnType<typeof vi.fn> {
  const spy = vi.fn(async () => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } }))
  vi.stubGlobal('fetch', spy)
  return spy
}

describe('createAutomation/resumeAutomation -- the client always sends the real request', () => {
  it('createAutomation calls POST /api/automations unconditionally, regardless of the calling component\'s own role check', async () => {
    const spy = respondWith({ automation: { id: 'a1' }, webhookToken: null }, 201)

    await createAutomation({ name: 'x', prompt: null, repos: [{ name: 'r', url: 'https://github.com/acme/r', branch: null }], triggerType: 'manual' })

    expect(spy).toHaveBeenCalledTimes(1)
    const [url, init] = spy.mock.calls[0] as [string, RequestInit]
    expect(url).toContain('/api/automations')
    expect(init.method).toBe('POST')
  })

  it('a 403 the server returns (a member, not admin/maintainer -- the REAL authz.ActionManageAutomations gate) surfaces as a genuine ApiError, never silently treated as success', async () => {
    respondWith({ error: 'forbidden' }, 403)

    await expect(createAutomation({ name: 'x', prompt: null, repos: [{ name: 'r', url: 'https://github.com/acme/r', branch: null }], triggerType: 'manual' })).rejects.toMatchObject({ status: 403 })
  })

  it('resumeAutomation calls POST on the real resume URL unconditionally', async () => {
    const spy = respondWith({ id: 'a1', status: 'active' }, 200)

    await resumeAutomation('a1')

    expect(spy).toHaveBeenCalledTimes(1)
    const [url, init] = spy.mock.calls[0] as [string, RequestInit]
    expect(url).toContain('/api/automations/a1/resume')
    expect(init.method).toBe('POST')
  })

  it('a 403 on resumeAutomation is likewise a real, typed error', async () => {
    respondWith({ error: 'forbidden' }, 403)
    await expect(resumeAutomation('a1')).rejects.toBeInstanceOf(ApiError)
  })
})
