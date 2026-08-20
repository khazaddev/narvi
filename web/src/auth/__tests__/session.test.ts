// Proves auth/session.ts's own installUnauthorizedHandler -- specifically
// the refetchType: 'none' fix for a real bug caught during this Step's
// own manual browser verification: the naive version (default
// refetchType, 'active') invalidated meQueryOptions on EVERY 401,
// including the one GET /api/me's own request produces -- which
// immediately re-fetched the SAME query, got 401 again, invalidated
// again, in an unbounded tight loop (dozens of requests observed live in
// one page load, before this fix). This test triggers the SAME real code
// path (http.ts's request<T> calling its own onUnauthorized hook on a
// real 401 response) rather than invoking the handler function directly,
// so it proves the actual wiring, not just the fix in isolation.
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { QueryClient } from '@tanstack/react-query'

import { installUnauthorizedHandler } from '../session'
import { getMe } from '../../api/endpoints'
import { authQueryKeys } from '../../api/queryKeys'

describe('installUnauthorizedHandler', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('invalidates the me query with refetchType: "none" on a real 401 -- never the default (which would immediately re-fetch, including GET /api/me itself)', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ error: 'unauthorized' }), {
          status: 401,
          headers: { 'Content-Type': 'application/json' },
        }),
      ),
    )

    const invalidateQueries = vi.fn().mockResolvedValue(undefined)
    const fakeQueryClient = { invalidateQueries } as unknown as QueryClient
    installUnauthorizedHandler(fakeQueryClient)

    await expect(getMe()).rejects.toThrow()

    expect(invalidateQueries).toHaveBeenCalledTimes(1)
    expect(invalidateQueries).toHaveBeenCalledWith({ queryKey: authQueryKeys.me(), refetchType: 'none' })
  })

  it('does not invalidate at all on a successful (non-401) response', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            id: 'user-1',
            email: 'octocat@example.com',
            displayName: 'Octocat',
            role: 'member',
            disabled: false,
            createdAt: '2026-01-01T00:00:00Z',
            identities: [],
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        ),
      ),
    )

    const invalidateQueries = vi.fn().mockResolvedValue(undefined)
    const fakeQueryClient = { invalidateQueries } as unknown as QueryClient
    installUnauthorizedHandler(fakeQueryClient)

    await expect(getMe()).resolves.toMatchObject({ id: 'user-1' })
    expect(invalidateQueries).not.toHaveBeenCalled()
  })
})
