// workflowRunAuthorization.test.ts -- the same "the affordance is not the
// real gate" proof planAuthorization.test.ts/workflowAuthorization.test.ts
// establish, applied to this Step's own new endpoint: decideWorkflowStep
// (api/endpoints.ts) is an unconditional network call with no client-side
// authorization logic of its own. The real gate is server-side
// (authz.ActionDecideWorkflowStep, own/joined-aware, the SAME matrix row as
// ActionApprovePlan, §25.11) -- already proven directly against a real
// Postgres instance by decideworkflowstep_integration_test.go. This file
// proves the client half: the request really goes out over the wire, with
// the real method/URL/body, and a 403/409 the server returns surfaces as a
// genuine, typed error rather than being silently swallowed or treated as
// success. Also covers the two plain reads (listSessionWorkflowRuns/
// getWorkflowRun), which carry no RBAC beyond signed-in (§25.10's own
// "session-read gate ... every role including viewer").
import { afterEach, describe, expect, it, vi } from 'vitest'

import { decideWorkflowStep, getWorkflowRun, listSessionWorkflowRuns } from '../../api/endpoints'
import { ApiError } from '../../api/http'

afterEach(() => {
  vi.unstubAllGlobals()
})

function respondWith(body: unknown, status: number): ReturnType<typeof vi.fn> {
  const spy = vi.fn(async () => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } }))
  vi.stubGlobal('fetch', spy)
  return spy
}

describe('listSessionWorkflowRuns / getWorkflowRun -- plain reads, real URLs', () => {
  it('listSessionWorkflowRuns calls GET on the real per-session URL', async () => {
    const spy = respondWith({ runs: [] }, 200)
    await listSessionWorkflowRuns('s1')
    expect(spy).toHaveBeenCalledTimes(1)
    const [url, init] = spy.mock.calls[0] as [string, RequestInit | undefined]
    expect(url).toBe('/api/sessions/s1/workflow-runs')
    expect(init?.method ?? 'GET').toBe('GET')
  })

  it('getWorkflowRun calls GET on the real per-run URL, which carries no sessionId of its own', async () => {
    const spy = respondWith({ run: { id: 'r1' }, stepRuns: [] }, 200)
    await getWorkflowRun('r1')
    expect(spy).toHaveBeenCalledTimes(1)
    const [url] = spy.mock.calls[0] as [string, RequestInit | undefined]
    expect(url).toBe('/api/workflow-runs/r1')
  })
})

describe('decideWorkflowStep -- the client always sends the real request, unconditionally', () => {
  it('calls POST on the real decide URL with the given verdict/text body, for approve', async () => {
    const spy = respondWith({ stepRunId: 'sr1', stepRunStatus: 'completed', runStatus: 'running', turnId: 't1' }, 200)

    await decideWorkflowStep('run1', 'sr1', { verdict: 'approve', text: null })

    expect(spy).toHaveBeenCalledTimes(1)
    const [url, init] = spy.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/workflow-runs/run1/steps/sr1/decide')
    expect(init.method).toBe('POST')
    expect(JSON.parse(init.body as string)).toEqual({ verdict: 'approve', text: null })
  })

  it('sends the real revise body, including the human text, unconditionally -- this function performs no blank-text check of its own (that is the component\'s courtesy and the server\'s own real guarantee, decideworkflowstep.go)', async () => {
    const spy = respondWith({ stepRunId: 'sr1', stepRunStatus: 'completed', runStatus: 'running', turnId: 't2' }, 200)

    await decideWorkflowStep('run1', 'sr1', { verdict: 'revise', text: 'please also add a test' })

    const [, init] = spy.mock.calls[0] as [string, RequestInit]
    expect(JSON.parse(init.body as string)).toEqual({ verdict: 'revise', text: 'please also add a test' })
  })

  it('sends the real reject body even for a session/run/step-run id triple this client has no opinion about -- proving no canActOnWorkflowStep-style gate lives at the call site itself', async () => {
    const spy = respondWith({ stepRunId: 'sr-unknown', stepRunStatus: 'failed', runStatus: 'failed', turnId: null }, 200)

    await decideWorkflowStep('any-run', 'any-step-run', { verdict: 'reject', text: null })

    expect(spy).toHaveBeenCalledTimes(1)
  })

  it('a 403 the server returns (the REAL "not own/joined" refusal) surfaces as a genuine ApiError -- never silently treated as success', async () => {
    respondWith({ error: "not authorized to decide this session's workflow step" }, 403)

    await expect(decideWorkflowStep('run1', 'sr1', { verdict: 'approve', text: null })).rejects.toMatchObject({ status: 403 })
  })

  it('a 409 the server returns (already decided, or a stale/mismatched id -- the guarded UPDATE) is likewise a real, typed error, never a false success', async () => {
    const message = 'workflow step is not awaiting decision (already decided, or a stale id)'
    respondWith({ error: message }, 409)

    await expect(decideWorkflowStep('run1', 'sr1', { verdict: 'reject', text: null })).rejects.toMatchObject({ status: 409, message })
  })

  it('a 400 the server returns for a blank revise (the server\'s own real guarantee, never just this client\'s courtesy check) surfaces as a genuine ApiError', async () => {
    respondWith({ error: 'revise requires non-empty text' }, 400)

    await expect(decideWorkflowStep('run1', 'sr1', { verdict: 'revise', text: '   ' })).rejects.toBeInstanceOf(ApiError)
  })
})
