// Proves auth/requireAuth.ts's own beforeLoad guard: the "an
// unauthenticated visitor hitting a deep link should land somewhere
// sensible" behavior (Step 81), and that a GENUINE failure (not a 401)
// is never silently folded into "not signed in".
import { describe, expect, it, vi } from 'vitest'
import { isRedirect } from '@tanstack/react-router'
import type { QueryClient } from '@tanstack/react-query'

import { requireAuth } from '../requireAuth'
import { ApiError } from '../../api/http'

// Stubs fetchQuery, which is what requireAuth calls -- see that file's own
// comment on why it is not ensureQueryData. These four cases cover the
// guard's BRANCHING (signed out vs valid vs genuine failure) and a stub is
// the right tool for that. They deliberately do not cover cache semantics:
// stubbing the client cannot express staleness or invalidation at all, which
// is exactly how a signed-out visitor once kept passing this guard. That half
// is pinned against a real QueryClient in requireAuthCache.test.ts.
function fakeQueryClient(fetchQuery: () => Promise<unknown>): QueryClient {
  return { fetchQuery } as unknown as QueryClient
}

describe('requireAuth', () => {
  it('redirects to /sign-in with next=<current path> when the visitor is signed out (401)', async () => {
    const queryClient = fakeQueryClient(() => Promise.reject(new ApiError(401, 'unauthorized', { error: 'unauthorized' })))

    let caught: unknown
    try {
      await requireAuth({ context: { queryClient }, location: { pathname: '/' } })
    } catch (err) {
      caught = err
    }

    expect(isRedirect(caught)).toBe(true)
    const redirectOptions = (caught as { options: { to?: string; search?: unknown } }).options
    expect(redirectOptions.to).toBe('/sign-in')
    expect(redirectOptions.search).toEqual({ next: '/' })
  })

  it('omits `next` when the current path is not a known route (isSafeReturnTo rejects it)', async () => {
    const queryClient = fakeQueryClient(() => Promise.reject(new ApiError(401, 'unauthorized', { error: 'unauthorized' })))

    let caught: unknown
    try {
      await requireAuth({ context: { queryClient }, location: { pathname: '/not-a-real-route' } })
    } catch (err) {
      caught = err
    }

    expect(isRedirect(caught)).toBe(true)
    const redirectOptions = (caught as { options: { search?: unknown } }).options
    expect(redirectOptions.search).toEqual({})
  })

  it('resolves without redirecting when a valid session exists', async () => {
    const queryClient = fakeQueryClient(() => Promise.resolve({ id: 'user-1' }))
    await expect(requireAuth({ context: { queryClient }, location: { pathname: '/' } })).resolves.toBeUndefined()
  })

  it('rethrows a genuine failure (500) instead of treating it as "not signed in" -- a visitor with a valid session must never be bounced to sign-in by a transient backend error', async () => {
    const backendFailure = new ApiError(500, 'internal error', { error: 'internal error' })
    const queryClient = fakeQueryClient(() => Promise.reject(backendFailure))

    const spy = vi.fn()
    await requireAuth({ context: { queryClient }, location: { pathname: '/' } }).catch(spy)

    expect(spy).toHaveBeenCalledWith(backendFailure)
    expect(isRedirect(backendFailure)).toBe(false)
  })
})
