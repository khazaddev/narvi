// workflowAuthorization.test.ts -- the same "the affordance is not the real
// gate" proof settingsAuthorization.test.ts/automationAuthorization.test.ts
// establish, applied to this screen's own new endpoints: createWorkflow
// Definition/putWorkflowDefinition/putWorkflowBinding (api/endpoints.ts) are
// unconditional network calls with no client-side authorization logic of
// their own. The real gate is server-side (authz.ActionManageWorkflow
// Definitions maintainer+, authz.ActionActivateWorkflowBinding admin-only,
// §25.11) -- already proven directly against a real Postgres instance by
// this package's own Go tests. This file proves the client half: the
// request really goes out over the wire, with the real method, and a 403 --
// or, for a structural refusal, a 409 -- surfaces as a genuine, typed error
// rather than being silently swallowed or treated as success.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { createWorkflowDefinition, putWorkflowBinding, putWorkflowDefinition } from '../../api/endpoints'
import { ApiError } from '../../api/http'

afterEach(() => {
  vi.unstubAllGlobals()
})

function respondWith(body: unknown, status: number): ReturnType<typeof vi.fn> {
  const spy = vi.fn(async () => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } }))
  vi.stubGlobal('fetch', spy)
  return spy
}

describe('putWorkflowBinding -- the client always sends the real, activation-changing request', () => {
  it('calls PUT /api/workflow-bindings unconditionally, regardless of the calling component\'s own role check', async () => {
    const spy = respondWith({ id: 'b1', lane: 'request', repoFullName: null, workflowDefinitionId: 'd1', definitionVersion: 1, createdAt: '2026-08-20T00:00:00Z', updatedAt: '2026-08-20T00:00:00Z' }, 200)

    await putWorkflowBinding({ lane: 'request', repoFullName: null, workflowDefinitionId: 'd1' })

    expect(spy).toHaveBeenCalledTimes(1)
    const [url, init] = spy.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/workflow-bindings')
    expect(init.method).toBe('PUT')
  })

  it('a 403 the server returns (a maintainer, not admin -- the REAL authz.ActionActivateWorkflowBinding gate) surfaces as a genuine ApiError, an authorization refusal, never silently treated as success', async () => {
    respondWith({ error: 'forbidden' }, 403)

    await expect(putWorkflowBinding({ lane: 'request', repoFullName: null, workflowDefinitionId: 'd1' })).rejects.toMatchObject({ status: 403 })
  })
})

describe('putWorkflowDefinition -- the client always sends the real request, and a structural refusal surfaces verbatim', () => {
  it('calls PUT /api/workflow-definitions/:id unconditionally, with the given whole-document body', async () => {
    const spy = respondWith({ id: 'd1', lane: 'request', name: 'x', isBuiltIn: false, version: 2, steps: [], createdAt: '2026-08-20T00:00:00Z', updatedAt: '2026-08-20T00:00:00Z' }, 200)

    await putWorkflowDefinition('d1', { name: 'x', steps: [] as never })

    expect(spy).toHaveBeenCalledTimes(1)
    const [url, init] = spy.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/workflow-definitions/d1')
    expect(init.method).toBe('PUT')
  })

  it('a 409 structural refusal (built-in / bound / has run history, §25.10/§25.11) surfaces with the server\'s own verbatim message, never a generic "save failed"', async () => {
    const message = 'workflow definition has run history: it has been used by at least one workflow run and cannot be edited or deleted -- duplicate it and edit the copy instead'
    respondWith({ error: message }, 409)

    await expect(putWorkflowDefinition('d1', { name: 'x', steps: [] as never })).rejects.toMatchObject({ status: 409, message })
  })

  it('a 403 (a member, not maintainer+ -- the REAL authz.ActionManageWorkflowDefinitions gate) surfaces as a genuine ApiError', async () => {
    respondWith({ error: 'forbidden' }, 403)
    await expect(putWorkflowDefinition('d1', { name: 'x', steps: [] as never })).rejects.toBeInstanceOf(ApiError)
  })
})

describe('createWorkflowDefinition -- the client always sends the real request, in either mode', () => {
  it('calls POST /api/workflow-definitions unconditionally for a duplicate-mode body', async () => {
    const spy = respondWith({ id: 'd2', lane: 'request', name: 'copy', isBuiltIn: false, version: 1, steps: [], createdAt: '2026-08-20T00:00:00Z', updatedAt: '2026-08-20T00:00:00Z' }, 201)

    await createWorkflowDefinition({ sourceDefinitionId: 'd1', name: 'copy' })

    expect(spy).toHaveBeenCalledTimes(1)
    const [url, init] = spy.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/workflow-definitions')
    expect(init.method).toBe('POST')
    expect(JSON.parse(init.body as string)).toMatchObject({ sourceDefinitionId: 'd1', name: 'copy' })
  })

  it('a 403 (a member, not maintainer+) surfaces as a genuine ApiError', async () => {
    respondWith({ error: 'forbidden' }, 403)
    await expect(createWorkflowDefinition({ sourceDefinitionId: 'd1', name: 'copy' })).rejects.toMatchObject({ status: 403 })
  })
})
