// planAuthorization.test.ts -- the task's own required "authorization
// test": proving the approval affordance is NOT the only thing gating
// approval. canActOnPlan (planFormat.ts) is a CLIENT-SIDE approximation
// used only to decide whether the approve/reject/request-changes buttons
// render enabled -- PlanModeView.tsx's own top doc comment is explicit
// that it is never the real gate. What actually protects an approve/
// reject call is internal/adapters/inbound/httpapi/planauthz.go's
// canActOnPlan, running server-side on every single request regardless of
// what this client rendered (already proven directly against a real
// Postgres instance by internal/adapters/inbound/httpapi/
// planapprove_integration_test.go's own TestApprovePlan_
// NonOwnerNonParticipantMember_Returns403/TestApprovePlan_Viewer_
// NotOwnerOrParticipant_Returns403).
//
// This file proves the CLIENT half of that split, at the one layer this
// package's test environment can actually exercise (plain Node, no DOM --
// see vitest.config.ts's own top comment; there is no jsdom/
// @testing-library/react anywhere in this codebase to simulate a real
// button click):
//   1. approvePlan/rejectPlan (api/endpoints.ts) send the REAL request
//      over the wire -- asserted against an actual fetch spy, not a
//      client-side short-circuit -- exactly the assertion the task brief
//      asks for ("assert the client SENDS the request").
//   2. Neither function contains any authorization check of its own: they
//      are unconditional network calls. canActOnPlan is never imported by
//      api/endpoints.ts at all (grepped directly, and asserted below by
//      construction -- a 403 from the fake server below still reaches the
//      caller as a real, typed error, proving nothing short-circuited it
//      client-side first).
//   3. A 403 the server returns surfaces as a genuine, typed ApiError
//      (status 403), never silently swallowed or reinterpreted as
//      success -- the "server's refusal is surfaced honestly" half of the
//      brief. PlanModeView.tsx's own ApprovalBar renders this exact
//      status code as an explicit "you're not authorized" message
//      (mutation.isError branch) rather than a generic failure or a
//      silent no-op.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { approvePlan, rejectPlan } from '../../api/endpoints'
import { ApiError } from '../../api/http'

afterEach(() => {
  vi.unstubAllGlobals()
})

function respondWith(body: unknown, status: number): ReturnType<typeof vi.fn> {
  const spy = vi.fn(async () => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } }))
  vi.stubGlobal('fetch', spy)
  return spy
}

describe('approvePlan/rejectPlan -- the client always sends the real request', () => {
  it('approvePlan calls POST on the real approve URL for this exact session/plan, unconditionally -- no client-side authorization check gates the call itself', async () => {
    const spy = respondWith({ planId: 'p1', status: 'approved', turnId: 't1' }, 200)

    await approvePlan('s1', 'p1')

    expect(spy).toHaveBeenCalledTimes(1)
    const [url, init] = spy.mock.calls[0] as [string, RequestInit]
    expect(url).toContain('/api/sessions/s1/plans/p1/approve')
    expect(init.method).toBe('POST')
  })

  it('rejectPlan calls POST on the real reject URL, unconditionally, the same way', async () => {
    const spy = respondWith({ planId: 'p1', status: 'rejected', turnId: null }, 200)

    await rejectPlan('s1', 'p1')

    expect(spy).toHaveBeenCalledTimes(1)
    const [url, init] = spy.mock.calls[0] as [string, RequestInit]
    expect(url).toContain('/api/sessions/s1/plans/p1/reject')
    expect(init.method).toBe('POST')
  })

  it('a 403 the server returns (the REAL "not own/joined" refusal, planauthz.go) surfaces as a genuine ApiError -- never silently treated as success, never swallowed', async () => {
    respondWith({ error: 'not authorized to act on this session\'s plans' }, 403)

    await expect(approvePlan('s1', 'p1')).rejects.toBeInstanceOf(ApiError)
    await expect(approvePlan('s1', 'p1')).rejects.toMatchObject({ status: 403 })
  })

  it('a 409 the server returns (already decided, or a stale plan id -- decideplan.go\'s own guarded UPDATE) is likewise a real, typed error, not a false "approved"', async () => {
    respondWith({ error: 'plan is not awaiting approval (already decided, or a stale id)' }, 409)

    await expect(rejectPlan('s1', 'p1')).rejects.toMatchObject({ status: 409 })
  })

  it('the approve request still fires even for a session/plan id pair this client has no opinion about -- proving the call site itself carries no canActOnPlan-style gate (the mutation is unconditional; only the CALLER -- PlanModeView\'s button -- decides whether to invoke it, and that decision is a UX nicety the server does not rely on)', async () => {
    const spy = respondWith({ planId: 'p-unknown', status: 'approved', turnId: 't1' }, 200)

    await approvePlan('any-session-id', 'any-plan-id')

    expect(spy).toHaveBeenCalledTimes(1)
  })
})
